#!/usr/bin/env python3
"""Read-only synthetic MCP smoke test; never prints credentials or result rows."""

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file_pointer, code, message, headers, new_url):
        raise RuntimeError("MCP endpoint redirected; refusing to forward credentials")


def endpoint(value):
    parsed = urllib.parse.urlparse(value)
    loopback = parsed.hostname in {"127.0.0.1", "localhost", "::1"}
    if parsed.scheme != "https" and not (parsed.scheme == "http" and loopback):
        raise ValueError("MCP endpoint must use HTTPS, except for loopback testing")
    if parsed.username or parsed.password or parsed.query or parsed.fragment or not parsed.hostname:
        raise ValueError("MCP endpoint must not contain credentials, query, or fragment")
    if parsed.path not in {"/mcp", "/mcp/2026-07-28"}:
        raise ValueError("MCP endpoint path must be /mcp or /mcp/2026-07-28")
    return value


class Client:
    def __init__(self, url, key, pace):
        self.url = url
        self.key = key
        self.pace = pace
        self.next_at = 0.0
        self.session = ""
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, number, method, params):
        self.next_at = max(time.monotonic(), self.next_at) + self.pace
        time.sleep(max(0, self.next_at - time.monotonic()))
        payload = json.dumps({"jsonrpc": "2.0", "id": number, "method": method, "params": params}).encode()
        headers = {
            "Authorization": "Bearer " + self.key,
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": "2025-06-18",
        }
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        request = urllib.request.Request(self.url, data=payload, headers=headers, method="POST")
        try:
            with self.opener.open(request, timeout=30) as response:
                self.session = response.headers.get("Mcp-Session-Id", self.session)
                body = response.read(1 << 20).decode("utf-8")
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"MCP HTTP {error.code}") from None
        if len(body) >= 1 << 20:
            raise RuntimeError("MCP response exceeded 1 MiB")
        if body.startswith("event:") or body.startswith("data:"):
            events = [line[5:].strip() for line in body.splitlines() if line.startswith("data:")]
            if not events:
                raise RuntimeError("MCP stream contained no response")
            body = events[-1]
        message = json.loads(body)
        if "error" in message:
            raise RuntimeError(f"MCP {method} returned a protocol error")
        return message["result"]

    def query(self, number, datasource, sql):
        result = self.request(number, "tools/call", {
            "name": "query", "arguments": {"datasource_id": datasource, "sql": sql},
        })
        content = result.get("content") or []
        texts = [item.get("text", "") for item in content if item.get("type") == "text"]
        if not texts:
            raise RuntimeError("query returned no text content")
        return json.loads(texts[0])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mcp-url", required=True)
    parser.add_argument("--datasource-id", required=True)
    parser.add_argument("--allow-sql", required=True)
    parser.add_argument("--deny-sql", required=True)
    parser.add_argument("--deny-rule", required=True)
    parser.add_argument("--min-masked-cells", type=int, default=1)
    parser.add_argument("--pace-seconds", type=float, default=1.1)
    arguments = parser.parse_args()
    if arguments.pace_seconds < 0 or arguments.min_masked_cells < 1:
        raise ValueError("pace and masking bounds must be non-negative")
    key = os.environ.get("AGENTSQL_MCP_ACCEPTANCE_KEY", "")
    if not key:
        raise ValueError("AGENTSQL_MCP_ACCEPTANCE_KEY is required")
    client = Client(endpoint(arguments.mcp_url), key, arguments.pace_seconds)
    client.request(1, "initialize", {
        "protocolVersion": "2025-06-18", "capabilities": {},
        "clientInfo": {"name": "agentsql-acceptance", "version": "1"},
    })
    listed = client.request(2, "tools/list", {})
    if "query" not in {tool.get("name") for tool in listed.get("tools", [])}:
        raise RuntimeError("query tool was not advertised")
    allowed = client.query(3, arguments.datasource_id, arguments.allow_sql)
    if allowed.get("decision") != "allow":
        raise RuntimeError("allow case was not allowed")
    data = allowed.get("data") or {}
    audit = data.get("audit_id")
    masked = (data.get("redact") or {}).get("MaskedCells", 0)
    rows = (data.get("result") or {}).get("Rows") or []
    if not isinstance(audit, int) or masked < arguments.min_masked_cells or not rows:
        raise RuntimeError("allow case lacked audit, masked cells, or rows")
    if any(re.fullmatch(r"\d{11}", str(cell)) for row in rows for cell in row):
        raise RuntimeError("allow case returned an unmasked phone-shaped value")
    denied = client.query(4, arguments.datasource_id, arguments.deny_sql)
    denied_data = denied.get("data") or {}
    denied_audit = denied_data.get("audit_id")
    if denied.get("decision") != "deny" or not isinstance(denied_audit, int):
        raise RuntimeError("deny case lacked rejection or audit ID")
    if not any(hit.get("RuleID") == arguments.deny_rule for hit in denied_data.get("hits", [])):
        raise RuntimeError("deny case did not return the expected rule")
    print(json.dumps({"status": "pass", "allow_audit_id": audit, "deny_audit_id": denied_audit,
                      "masked_cells": masked, "deny_rule": arguments.deny_rule}, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, KeyError, json.JSONDecodeError) as error:
        print(f"MCP_ACCEPTANCE_FAIL={error}", file=sys.stderr)
        sys.exit(1)
