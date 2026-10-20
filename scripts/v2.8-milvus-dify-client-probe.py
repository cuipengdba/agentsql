"""Exercise controlled Milvus search through Dify 1.17.1's real MCPClient."""

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
    for raw in ("13800138000", "13900139000", "13700137000"):
        if raw in encoded:
            raise AssertionError("unmasked synthetic data reached Dify")
    if decision == "allow":
        if "****" not in encoded or payload.get("redact", {}).get("MaskedCells", 0) < 1:
            raise AssertionError("masked Milvus phone was not returned")
    elif '"Rows"' in encoded:
        raise AssertionError("denied Milvus search returned rows")
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
        if "search_vectors" not in {tool.name for tool in client.list_tools()}:
            raise AssertionError("Dify did not discover search_vectors")
        base = {"datasource_id": datasource, "vector_field": "embedding",
                "vector": [0.1234567, 0.5, 0, 0], "fields": ["name", "phone"], "top_k": 2}
        allowed = check(client.invoke_tool("search_vectors", {**base, "collection": "events"}), "allow")
        denied = check(client.invoke_tool("search_vectors", {**base, "collection": "private_notes"}), "deny")
        print(json.dumps({"client": "Dify MCPClient 1.17.1", "allow_audit": allowed,
                          "deny_audit": denied}, ensure_ascii=False))
        print("V280_DIFY_MILVUS=PASS")
    finally:
        try:
            client.cleanup()
        except ValueError as error:
            print(f"DIFY_SESSION_CLEANUP_WARNING={type(error).__name__}")


if __name__ == "__main__":
    main()
