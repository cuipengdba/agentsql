"""Exercise bounded ClickHouse reads through Dify 1.17.1's real MCPClient."""

import json
import os

from core.mcp.mcp_client import MCPClient


def required(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError(f"missing test setting: {name}")
    return value


def check(response, decision: str) -> int:
    data = response.structuredContent
    if not isinstance(data, dict) or data.get("decision") != decision:
        raise AssertionError(f"unexpected MCP decision: {decision}")
    payload = data.get("data")
    if not isinstance(payload, dict) or not isinstance(payload.get("audit_id"), int):
        raise AssertionError("missing durable audit ID")
    encoded = json.dumps(response.model_dump(), ensure_ascii=False)
    if "13812345678" in encoded or "synthetic-secret-canary" in encoded:
        raise AssertionError("unmasked synthetic data reached Dify")
    if decision == "allow":
        if "138****5678" not in encoded or payload.get("redact", {}).get("MaskedCells", 0) < 1:
            raise AssertionError("masked ClickHouse phone was not returned")
    elif "138****5678" in encoded:
        raise AssertionError("denied ClickHouse query returned a row")
    return payload["audit_id"]


def main() -> None:
    client = MCPClient(
        server_url=required("AGENTSQL_TEST_MCP_URL"),
        headers={"Authorization": f"Bearer {required('AGENTSQL_TEST_MCP_KEY')}",
                 "MCP-Protocol-Version": "2025-06-18"},
        timeout=30,
        sse_read_timeout=30,
    )
    datasource = required("AGENTSQL_TEST_DATASOURCE_ID")
    try:
        client._initialize()
        if "query_analytics" not in {tool.name for tool in client.list_tools()}:
            raise AssertionError("Dify did not discover query_analytics")
        base = {"datasource_id": datasource, "fields": ["name", "phone"],
                "filter": {"region": "CN"}, "limit": 2}
        allowed = check(client.invoke_tool("query_analytics", {**base, "table": "events"}), "allow")
        denied = check(client.invoke_tool("query_analytics", {**base, "table": "private_notes"}), "deny")
        print(json.dumps({"client": "Dify MCPClient 1.17.1", "allow_audit": allowed,
                          "deny_audit": denied}, ensure_ascii=False))
        print("V380_DIFY_CLICKHOUSE=PASS")
    finally:
        try:
            client.cleanup()
        except ValueError as error:
            print(f"DIFY_SESSION_CLEANUP_WARNING={type(error).__name__}")


if __name__ == "__main__":
    main()
