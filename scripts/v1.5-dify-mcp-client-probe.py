"""Run a bounded live query probe through Dify 1.17.1's own MCPClient.

Only synthetic fixture data is accepted. Credentials arrive through temporary
environment variables and are never printed by this script.
"""

import json
import os

from core.mcp.mcp_client import MCPClient


def required(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError(f"missing test setting: {name}")
    return value


def check_result(result, expected_decision: str, rule: str | None = None) -> dict:
    response = result.structuredContent
    if not isinstance(response, dict) or response.get("decision") != expected_decision:
        raise AssertionError(f"unexpected MCP decision: {response!r}")
    data = response.get("data")
    if not isinstance(data, dict) or not isinstance(data.get("audit_id"), int):
        raise AssertionError("MCP response has no audit ID")
    serialized = json.dumps(result.model_dump(), ensure_ascii=False)
    if "13812345678" in serialized or "13800135678" in serialized or "13800105678" in serialized:
        raise AssertionError("unmasked synthetic phone reached Dify")
    if expected_decision == "allow":
        if data.get("redact", {}).get("MaskedCells") != 1:
            raise AssertionError("Dify did not receive the phone mask")
        if "138****5678" not in serialized:
            raise AssertionError("Dify did not receive the masked synthetic phone")
    if expected_decision == "deny":
        if rule and not any(hit.get("RuleID") == rule for hit in data.get("hits", [])):
            raise AssertionError(f"Dify did not receive expected policy rule {rule}")
        if "138****5678" in serialized:
            raise AssertionError("denied Dify query unexpectedly returned a row")
    return {"decision": expected_decision, "audit_id": data["audit_id"], "rule": rule}


def main() -> None:
    url = required("AGENTSQL_TEST_MCP_URL")
    key = required("AGENTSQL_TEST_MCP_KEY")
    datasource = required("AGENTSQL_TEST_DATASOURCE_ID")
    allow_sql = required("AGENTSQL_TEST_ALLOW_SQL")
    deny_sql = required("AGENTSQL_TEST_DENY_SQL")
    deny_rule = required("AGENTSQL_TEST_DENY_RULE")
    client = MCPClient(
        server_url=url,
        headers={"Authorization": f"Bearer {key}", "MCP-Protocol-Version": "2025-06-18"},
        timeout=30,
        sse_read_timeout=30,
    )
    try:
        client._initialize()
        if "query" not in {tool.name for tool in client.list_tools()}:
            raise AssertionError("Dify did not discover AgentSQL query tool")
        allowed = check_result(client.invoke_tool("query", {"datasource_id": datasource, "sql": allow_sql}), "allow")
        denied = check_result(
            client.invoke_tool("query", {"datasource_id": datasource, "sql": deny_sql}), "deny", deny_rule
        )
        print(json.dumps({"client": "Dify MCPClient 1.17.1", "allow": allowed, "deny": denied}, ensure_ascii=False))
        print("V150_DIFY_MCP_CLIENT=PASS")
    finally:
        try:
            client.cleanup()
        except ValueError as error:
            # Dify 1.17.1 sometimes reports HTTP 204 on session termination
            # even after both tool calls succeeded; keep the warning explicit.
            print(f"DIFY_SESSION_CLEANUP_WARNING={type(error).__name__}")


if __name__ == "__main__":
    main()
