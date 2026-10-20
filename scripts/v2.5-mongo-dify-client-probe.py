"""Check the scoped MongoDB tool through Dify 1.17.1's real MCPClient."""

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
        if "138****5678" not in encoded or payload.get("redact", {}).get("MaskedCells") != 1:
            raise AssertionError("masked MongoDB phone was not returned")
    elif "138****5678" in encoded:
        raise AssertionError("denied MongoDB read returned a row")
    return payload["audit_id"]


def main() -> None:
    url = required("AGENTSQL_TEST_MCP_URL")
    key = required("AGENTSQL_TEST_MCP_KEY")
    datasource = required("AGENTSQL_TEST_DATASOURCE_ID")
    client = MCPClient(
        server_url=url,
        headers={"Authorization": f"Bearer {key}", "MCP-Protocol-Version": "2025-06-18"},
        timeout=30,
        sse_read_timeout=30,
    )
    try:
        client._initialize()
        if "find_documents" not in {tool.name for tool in client.list_tools()}:
            raise AssertionError("Dify did not discover find_documents")
        base = {"datasource_id": datasource, "fields": ["name", "phone"],
                "filter": {"name": "alice"}, "limit": 1}
        allowed = check(client.invoke_tool("find_documents", {**base, "collection": "customers"}), "allow")
        denied = check(client.invoke_tool("find_documents", {**base, "collection": "private_collection"}), "deny")
        print(json.dumps({"client": "Dify MCPClient 1.17.1", "allow_audit": allowed,
                          "deny_audit": denied}, ensure_ascii=False))
        print("V250_DIFY_MONGO=PASS")
    finally:
        try:
            client.cleanup()
        except ValueError as error:
            print(f"DIFY_SESSION_CLEANUP_WARNING={type(error).__name__}")


if __name__ == "__main__":
    main()
