"""SQLBot v1.10.2 datasource adapter. All business access goes through AgentSQL.

This module uses only the Python standard library. It never opens a database
connection, forwards SQLBot passwords, retries SQL, or falls back to a driver.
"""
from __future__ import annotations

import base64
from decimal import Decimal, InvalidOperation
import hashlib
import json
import math
import os
import re
import ssl
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from types import SimpleNamespace


class AdapterError(RuntimeError):
    """Sanitized failure safe to show in SQLBot's error surface."""


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise AdapterError("AgentSQL redirect refused")


_pace_lock = threading.Lock()
_next_request: dict[str, float] = {}


def deny_direct(*args, **kwargs):
    raise AdapterError("Direct business database connections are disabled by the AgentSQL adapter")


def _binding(ds):
    try:
        path = Path(os.environ["AGENTSQL_SQLBOT_CONFIG"])
        config = json.loads(path.read_text(encoding="utf-8"))
        if config.get("version") != 1 or not isinstance(config.get("bindings"), list):
            raise ValueError()
        identity = (getattr(ds, "oid", None), getattr(ds, "name", None))
        matches = [b for b in config["bindings"]
                   if (b.get("workspace_id"), b.get("datasource_name")) == identity]
        if len(matches) != 1:
            raise AdapterError("AgentSQL datasource/workspace mapping is missing or ambiguous")
        binding = matches[0]
        if getattr(ds, "type", None) != "pg" or binding.get("dialect") != "pg":
            raise AdapterError("This adapter requires a PostgreSQL datasource mapping")
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", binding.get("schema", "")):
            raise ValueError()
        if not binding.get("datasource_id") or not re.fullmatch(r"[A-Z_][A-Z0-9_]*", binding.get("key_env", "")):
            raise ValueError()
        return config, binding
    except AdapterError:
        raise
    except (OSError, ValueError, KeyError, TypeError):
        raise AdapterError("AgentSQL adapter configuration is missing or invalid") from None


class Client:
    def __init__(self, config, binding):
        self.url = config.get("endpoint", "")
        parsed = urllib.parse.urlparse(self.url)
        local = parsed.hostname in {"localhost", "127.0.0.1", "::1"}
        if (parsed.scheme != "https" and not (parsed.scheme == "http" and local)
                or not parsed.hostname or parsed.path != "/mcp"
                or parsed.username or parsed.password or parsed.query or parsed.fragment):
            raise AdapterError("AgentSQL endpoint must be HTTPS /mcp; HTTP is allowed only on loopback")
        self.key = os.environ.get(binding["key_env"], "")
        if not self.key or any(c in self.key for c in "\r\n"):
            raise AdapterError("AgentSQL Agent Key is missing or invalid")
        try:
            self.interval = float(config.get("min_interval_seconds", 0.6))
            self.timeout = float(config.get("timeout_seconds", 30))
            if not (math.isfinite(self.interval) and 0 <= self.interval <= 60
                    and math.isfinite(self.timeout) and 1 <= self.timeout <= 120):
                raise ValueError()
            context = ssl.create_default_context(cafile=config.get("ca_file") or None)
        except (ValueError, TypeError, OSError):
            raise AdapterError("AgentSQL TLS/timeout configuration is invalid") from None
        # Avoid ambient proxies for bearer credentials; operators configure the endpoint itself.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect(),
                                                  urllib.request.HTTPSHandler(context=context))
        self.session = ""
        self.number = 0
        self.pace_id = hashlib.sha256(self.key.encode()).hexdigest()

    def _http(self, payload=None, method="POST"):
        headers = {"Authorization": "Bearer " + self.key, "Content-Type": "application/json",
                   "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-06-18"}
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        with _pace_lock:
            wait = max(0, _next_request.get(self.pace_id, 0) - time.monotonic())
            time.sleep(wait)
            _next_request[self.pace_id] = time.monotonic() + self.interval
        request = urllib.request.Request(self.url,
            data=json.dumps(payload).encode() if payload is not None else None, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                self.session = response.headers.get("Mcp-Session-Id", self.session)
                raw = response.read(8 * 1024 * 1024 + 1)
                content_type = response.headers.get("Content-Type", "")
        except urllib.error.HTTPError as error:
            raise AdapterError(f"AgentSQL HTTP {error.code}; request was not retried") from None
        except (urllib.error.URLError, TimeoutError, OSError, ValueError):
            raise AdapterError("AgentSQL connection failed; request was not retried") from None
        if len(raw) > 8 * 1024 * 1024:
            raise AdapterError("AgentSQL response exceeded adapter limit")
        if not raw:
            return None
        try:
            text = raw.decode("utf-8")
            if "text/event-stream" in content_type:
                events = [line[5:].strip() for line in text.splitlines() if line.startswith("data:")]
                messages = [json.loads(event) for event in events if event and event != "[DONE]"]
                matches = [m for m in messages if m.get("id") == (payload or {}).get("id")]
                if len(matches) != 1:
                    raise ValueError()
                return matches[0]
            return json.loads(text)
        except (ValueError, UnicodeError, TypeError):
            raise AdapterError("AgentSQL returned an invalid protocol response") from None

    def rpc(self, method, params):
        self.number += 1
        result = self._http({"jsonrpc": "2.0", "id": self.number, "method": method, "params": params})
        if (not isinstance(result, dict) or result.get("id") != self.number
                or "error" in result or "result" not in result):
            raise AdapterError("AgentSQL protocol request failed")
        return result["result"]

    def __enter__(self):
        result = self.rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
            "clientInfo": {"name": "agentsql-sqlbot", "version": "1"}})
        if not isinstance(result, dict) or result.get("protocolVersion") != "2025-06-18" or not self.session:
            raise AdapterError("AgentSQL MCP protocol/session negotiation failed")
        self._http({"jsonrpc": "2.0", "method": "notifications/initialized"})
        return self

    def __exit__(self, *args):
        if self.session:
            try:
                self._http(method="DELETE")
            except AdapterError:
                pass  # Expiring transport sessions never trigger a query retry.

    def tool(self, name, arguments):
        wire = self.rpc("tools/call", {"name": name, "arguments": arguments})
        try:
            if not isinstance(wire, dict) or wire.get("isError"):
                raise ValueError()
            values = [item["text"] for item in wire["content"] if item.get("type") == "text"]
            if len(values) != 1:
                raise ValueError()
            envelope = json.loads(values[0])
            if not isinstance(envelope, dict):
                raise ValueError()
        except (KeyError, ValueError, TypeError):
            raise AdapterError("AgentSQL tool failed or returned an invalid result") from None
        if envelope.get("decision") != "allow":
            data = envelope.get("data")
            audit = data.get("audit_id") if isinstance(data, dict) else None
            suffix = f"; audit_id={audit}" if type(audit) is int and audit > 0 else ""
            raise AdapterError("AgentSQL did not allow this operation" + suffix)
        return envelope.get("data")


def _query_result(data, sql, origin_column, numeric_columns=()):
    if not isinstance(data, dict) or type(data.get("audit_id")) is not int or data["audit_id"] <= 0:
        raise AdapterError("AgentSQL query lacks a durable audit ID")
    result = data.get("result") or {}
    if not isinstance(result, dict):
        raise AdapterError("AgentSQL query result is invalid")
    columns, rows = result.get("Columns"), result.get("Rows")
    if not isinstance(columns, list) or not columns or not all(isinstance(c, str) and c for c in columns):
        raise AdapterError("AgentSQL result columns are invalid")
    names = columns if origin_column else [c.lower() for c in columns]
    if len(set(names)) != len(names):
        raise AdapterError("Use unique SQL column aliases for SQLBot")
    if rows is None:
        rows = []
    if not isinstance(rows, list) or any(not isinstance(r, list) or len(r) != len(names) for r in rows):
        raise AdapterError("AgentSQL result rows are invalid")
    rows = [list(row) for row in rows]
    # The gateway's legacy result contract uses strings. Only explicitly named
    # chart aliases may be converted; never guess types from phone/ID-shaped text.
    for i, name in enumerate(names):
        if name not in numeric_columns:
            continue
        converted = []
        try:
            for row in rows:
                value = row[i]
                if value is None:
                    converted.append(None)
                    continue
                if not isinstance(value, str) or not re.fullmatch(r"-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?", value):
                    raise ValueError()
                decimal = Decimal(value)
                number = int(decimal) if decimal == decimal.to_integral_value() else float(decimal)
                if abs(decimal) > 2**53 - 1 or not math.isfinite(number) or Decimal(str(number)) != decimal:
                    raise ValueError()
                converted.append(number)
        except (ValueError, InvalidOperation, OverflowError):
            continue  # Masked or precision-sensitive columns remain strings.
        for row, value in zip(rows, converted):
            row[i] = value
    numeric = lambda value: type(value) in (int, float) and (not isinstance(value, float) or math.isfinite(value))
    fields_info = []
    for i, name in enumerate(names):
        present = [row[i] for row in rows if row[i] is not None]
        fields_info.append({"name": name, "is_numeric": bool(present) and all(numeric(v) for v in present)})
    return {"fields": names, "data": [dict(zip(names, row)) for row in rows], "fields_info": fields_info,
            "sql": base64.b64encode(sql.encode()).decode(), "agentsql": {"audit_id": data["audit_id"]}}


def dispatch(operation, ds, *, sql=None, origin_column=False, table_name=None, is_raise=False, **kwargs):
    config, binding = _binding(ds)
    with Client(config, binding) as client:
        sources = client.tool("list_datasources", {})
        if not isinstance(sources, list) or not any(s.get("id") == binding["datasource_id"]
                and s.get("db_type") == "postgres" for s in sources if isinstance(s, dict)):
            raise AdapterError("AgentSQL PostgreSQL datasource is not authorized for this Agent")
        if operation == "check_connection":
            return True
        if operation == "get_version":
            return ""  # No fabricated server version or system-function bypass.
        if operation == "exec_sql":
            if not isinstance(sql, str) or not sql.strip():
                raise AdapterError("SQL is required")
            data = client.tool("query", {"datasource_id": binding["datasource_id"], "sql": sql})
            numeric_columns = binding.get("numeric_columns", [])
            if not isinstance(numeric_columns, list) or not all(isinstance(c, str) for c in numeric_columns):
                raise AdapterError("AgentSQL numeric alias configuration is invalid")
            return _query_result(data, sql, origin_column, numeric_columns)
        if operation not in {"get_schema", "get_tables", "get_fields"}:
            raise AdapterError("Unsupported SQLBot adapter operation")
        arguments = {"datasource_id": binding["datasource_id"]}
        if table_name:
            if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", table_name):
                raise AdapterError("SQLBot table name must be a simple identifier")
            arguments["table"] = binding["schema"] + "." + table_name
        columns = client.tool("list_schema", arguments)
        if not isinstance(columns, list):
            raise AdapterError("AgentSQL schema result is invalid")
        columns = [c for c in columns if isinstance(c, dict) and c.get("schema") == binding["schema"]]
        if operation == "get_schema":
            return sorted({c["schema"] for c in columns})
        if operation == "get_tables":
            return [SimpleNamespace(tableName=name, tableComment="") for name in sorted({c["table"] for c in columns})]
        if not table_name:
            raise AdapterError("A table name is required for SQLBot field discovery")
        if any(not c.get("data_type") for c in columns):
            raise AdapterError("AgentSQL schema types are required; upgrade to v3.0.2 or later")
        return [SimpleNamespace(fieldName=c["column"], fieldType=c["data_type"], fieldComment="")
                for c in columns if c.get("table") == table_name]
