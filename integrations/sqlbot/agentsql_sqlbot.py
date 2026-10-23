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

    def __init__(self, message, *, code="adapter_error", decision=None, audit_id=None,
                 approval_id=None, rules=()):
        super().__init__(message)
        self.code = code
        self.decision = decision
        self.audit_id = audit_id
        self.approval_id = approval_id
        self.rules = list(rules)

    def feedback(self):
        return {key: value for key, value in {
            "message": str(self), "code": self.code, "decision": self.decision,
            "audit_id": self.audit_id, "approval_id": self.approval_id,
            "rules": self.rules, "retryable": False,
        }.items() if value is not None}


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise AdapterError("AgentSQL redirect refused")


_pace_lock = threading.Lock()
_next_request: dict[str, float] = {}


def deny_direct(*args, **kwargs):
    raise AdapterError("Direct business database connections are disabled by the AgentSQL adapter")


def _read_config():
    try:
        path = Path(os.environ["AGENTSQL_SQLBOT_CONFIG"])
        config = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(config, dict) or config.get("version") not in (1, 2) or not isinstance(config.get("bindings"), list):
            raise ValueError()
        if not all(isinstance(b, dict) for b in config["bindings"]):
            raise ValueError()
        return config
    except (OSError, ValueError, KeyError, TypeError):
        raise AdapterError("AgentSQL adapter configuration is missing or invalid", code="configuration_invalid") from None


def _binding(ds):
    try:
        config = _read_config()
        identity = (getattr(ds, "oid", None), getattr(ds, "name", None))
        matches = [b for b in config["bindings"]
                   if (b.get("workspace_id"), b.get("datasource_name")) == identity]
        if config["version"] == 2:
            from agentsql_sqlbot_context import current_identity
            user = current_identity()
            workspace = _sqlbot_id(identity[0])
            if _sqlbot_id(user.oid) != workspace:
                raise AdapterError("AgentSQL workspace does not match the signed-in SQLBot user", code="identity_mismatch")
            matches = [b for b in config["bindings"] if _sqlbot_id(b.get("workspace_id")) == workspace
                       and b.get("datasource_name") == identity[1] and _sqlbot_id(b.get("sqlbot_user_id")) == _sqlbot_id(user.id)]
        if len(matches) != 1:
            raise AdapterError("AgentSQL datasource/workspace/user mapping is missing or ambiguous", code="mapping_missing")
        binding = dict(matches[0])
        if getattr(ds, "type", None) != "pg":
            raise AdapterError("This adapter requires a PostgreSQL datasource mapping")
        if config["version"] == 2:
            binding = _resolve(config, binding, user)
        if binding.get("dialect") != "pg":
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


def _sqlbot_id(value):
    if type(value) is int:
        value = str(value)
    if not isinstance(value, str) or not re.fullmatch(r"[1-9][0-9]{0,18}", value) or int(value) > 2**63 - 1:
        raise AdapterError("SQLBot identifiers must be exact positive decimal integers", code="configuration_invalid")
    return value


def _environment_secret(name):
    if not isinstance(name, str) or not re.fullmatch(r"[A-Z_][A-Z0-9_]*", name):
        raise AdapterError("AgentSQL secret environment variable name is invalid", code="configuration_invalid")
    value = os.environ.get(name, "")
    if not value or len(value) > 16384 or any(c.isspace() for c in value):
        raise AdapterError("AgentSQL credential is missing or invalid", code="credential_missing")
    return value


def _transport(config):
    url = config.get("endpoint", "")
    if not isinstance(url, str):
        raise AdapterError("AgentSQL endpoint is invalid")
    parsed = urllib.parse.urlparse(url)
    local = parsed.hostname in {"localhost", "127.0.0.1", "::1"}
    if (parsed.scheme != "https" and not (parsed.scheme == "http" and local)
            or not parsed.hostname or parsed.path != "/mcp"
            or parsed.username or parsed.password or parsed.query or parsed.fragment):
        raise AdapterError("AgentSQL endpoint must be HTTPS /mcp; HTTP is allowed only on loopback")
    try:
        timeout = float(config.get("timeout_seconds", 30))
        if not math.isfinite(timeout) or not 1 <= timeout <= 120:
            raise ValueError()
        context = ssl.create_default_context(cafile=config.get("ca_file") or None)
    except (ValueError, TypeError, OSError):
        raise AdapterError("AgentSQL TLS/timeout configuration is invalid") from None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect(),
                                        urllib.request.HTTPSHandler(context=context))
    return url, opener, timeout


def _resolve(config, binding, user):
    """Exchange one connector credential and a server-authenticated user for a short lease.

    No Agent Key or business database credential is returned by this endpoint.
    A fresh exchange is made for each operation; binding revocation is also
    checked by the gateway on every subsequent MCP request.
    """
    url, opener, timeout = _transport(config)
    identifier = binding.get("id")
    if not isinstance(identifier, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", identifier):
        raise AdapterError("AgentSQL binding ID is invalid", code="configuration_invalid")
    secret = _environment_secret(binding.get("connector_token_env"))
    _environment_secret(binding.get("agent_key_env"))
    payload = {"binding_id": identifier, "workspace_id": _sqlbot_id(user.oid),
               "datasource_name": binding.get("datasource_name"), "sqlbot_user_id": _sqlbot_id(user.id)}
    request = urllib.request.Request(url[:-4] + "/api/v1/integrations/sqlbot/resolve",
        data=json.dumps(payload).encode(), method="POST",
        headers={"Content-Type": "application/json", "Accept": "application/json",
                 "Authorization": "Bearer " + secret})
    try:
        with opener.open(request, timeout=timeout) as response:
            raw = response.read(65537)
        if len(raw) > 65536:
            raise ValueError()
        envelope = json.loads(raw)
        data = envelope.get("data") if isinstance(envelope, dict) and envelope.get("code") == 0 else None
        if not isinstance(data, dict) or data.get("binding_id") != identifier:
            raise ValueError()
        if type(data.get("revision")) is not int or data["revision"] < 1:
            raise ValueError()
        token = data.get("user_token")
        if not isinstance(token, str) or not token or len(token) > 16384 or any(c.isspace() for c in token):
            raise ValueError()
        if not isinstance(data.get("agent_id"), str) or not data["agent_id"]:
            raise ValueError()
        if not isinstance(data.get("datasource_id"), str) or not data["datasource_id"] or not isinstance(data.get("schema"), str):
            raise ValueError()
        columns = data.get("numeric_columns", [])
        if not isinstance(columns, list) or not all(isinstance(c, str) for c in columns):
            raise ValueError()
    except urllib.error.HTTPError as error:
        raise AdapterError(f"AgentSQL binding authorization failed (HTTP {error.code}); check mapping, identity and credentials",
                           code="binding_rejected") from None
    except (urllib.error.URLError, TimeoutError, OSError):
        raise AdapterError("AgentSQL binding service is unavailable; request was not retried", code="gateway_unavailable") from None
    except (ValueError, TypeError, KeyError):
        raise AdapterError("AgentSQL binding service returned an invalid response", code="protocol_invalid") from None
    return dict(binding, **{k: data[k] for k in ("agent_id", "datasource_id", "schema", "revision", "user_token")},
                numeric_columns=columns, dialect="pg", key_env=binding["agent_key_env"])


class Client:
    def __init__(self, config, binding):
        self.url, self.opener, self.timeout = _transport(config)
        self.key = _environment_secret(binding.get("key_env"))
        self.user_token = binding.get("user_token")
        try:
            self.interval = float(config.get("min_interval_seconds", 0.6))
            if not math.isfinite(self.interval) or not 0 <= self.interval <= 60:
                raise ValueError()
        except (ValueError, TypeError, OSError):
            raise AdapterError("AgentSQL TLS/timeout configuration is invalid") from None
        self.session = ""
        self.number = 0
        self.pace_id = hashlib.sha256(self.key.encode()).hexdigest()

    def _http(self, payload=None, method="POST"):
        headers = {"Authorization": "Bearer " + self.key, "Content-Type": "application/json",
                   "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-06-18"}
        if self.user_token:
            headers["X-AgentSQL-User-Authorization"] = "Bearer " + self.user_token
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
            "clientInfo": {"name": "agentsql-sqlbot", "version": "2"}})
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
            approval = data.get("approval_id") if isinstance(data, dict) else None
            approval = approval if isinstance(approval, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,128}", approval) else None
            if name == "request_approval" and envelope.get("decision") == "approve" and approval and type(audit) is int and audit > 0:
                return {"approval_id": approval, "audit_id": audit, "status": "pending"}
            suffix = f"; audit_id={audit}" if type(audit) is int and audit > 0 else ""
            hits = data.get("hits", []) if isinstance(data, dict) else []
            rules = sorted({h.get("rule_id") for h in hits if isinstance(h, dict)
                            and isinstance(h.get("rule_id"), str) and re.fullmatch(r"R[0-9]{3}", h["rule_id"])}) if isinstance(hits, list) else []
            decision = envelope.get("decision")
            decision = decision if decision in {"deny", "warn", "approve", "error"} else "unknown"
            if approval:
                suffix += "; approval_id=" + approval + "; open AgentSQL approvals to review this sealed request"
            if rules:
                suffix += "; rules=" + ",".join(rules)
            raise AdapterError("AgentSQL did not allow this operation" + suffix,
                               code="decision_" + decision, decision=decision,
                               audit_id=audit if type(audit) is int and audit > 0 else None,
                               approval_id=approval, rules=rules)
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
        if operation in {"request_approval", "get_approval_result", "execute_approved"}:
            if config["version"] != 2:
                raise AdapterError("Approval actions require a managed personal binding")
            if operation == "request_approval":
                if not isinstance(sql, str) or not 1 <= len(sql) <= 65536:
                    raise AdapterError("A bounded SQL statement is required")
                reason = kwargs.get("reason")
                if not isinstance(reason, str) or not 1 <= len(reason.strip()) <= 2000:
                    raise AdapterError("An approval reason is required")
                return client.tool(operation, {"datasource_id": binding["datasource_id"], "sql": sql, "reason": reason})
            approval_id = kwargs.get("approval_id", "")
            if not isinstance(approval_id, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", approval_id):
                raise AdapterError("A valid approval ID is required")
            data = client.tool(operation, {"approval_id": approval_id})
            if operation == "execute_approved":
                return _query_result(data, "", origin_column, binding.get("numeric_columns", []))
            return data
        if operation == "exec_sql":
            if not isinstance(sql, str) or not sql.strip():
                raise AdapterError("SQL is required")
            data = client.tool("query", {"datasource_id": binding["datasource_id"], "sql": sql})
            numeric_columns = binding.get("numeric_columns", [])
            if not isinstance(numeric_columns, list) or not all(isinstance(c, str) for c in numeric_columns):
                raise AdapterError("AgentSQL numeric alias configuration is invalid")
            return _query_result(data, sql, origin_column, numeric_columns)
        if operation not in {"get_schema", "get_tables", "get_fields", "authorized_schema"}:
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
        if operation == "authorized_schema":
            return columns
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
