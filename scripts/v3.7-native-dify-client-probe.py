"""Exercise v3.7 native reads through Dify 1.17.1's real MCPClient."""

import json
import os

from core.mcp.mcp_client import MCPClient


def required(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError(f"missing test setting: {name}")
    return value


def check(response, decision: str, masked: str, raw: str) -> int:
    content = response.structuredContent
    if not isinstance(content, dict) or content.get("decision") != decision:
        raise AssertionError(f"unexpected MCP decision: {decision}")
    payload = content.get("data")
    if not isinstance(payload, dict) or not isinstance(payload.get("audit_id"), int):
        raise AssertionError("missing durable audit ID")
    encoded = json.dumps(response.model_dump(), ensure_ascii=False)
    if raw in encoded:
        raise AssertionError("unmasked native data reached Dify")
    if decision == "allow":
        if masked not in encoded or payload.get("redact", {}).get("MaskedCells") != 1:
            raise AssertionError("masked native data did not reach Dify")
    elif masked in encoded:
        raise AssertionError("denied native read returned a value")
    return payload["audit_id"]


def main() -> None:
    kind = required("AGENTSQL_V370_CLIENT_KIND")
    datasource = required("AGENTSQL_TEST_DATASOURCE_ID")
    if kind == "mongo":
        tool = "find_documents"
        base = {"datasource_id": datasource, "fields": ["customer_id", "phone"],
                "filter": {"customer_id": "C7"}, "limit": 1}
        allowed = {**base, "collection": "customers"}
        denied = {**base, "collection": "private_collection"}
        masked, raw = "138****0007", "13800000007"
    elif kind in {"redis", "valkey"}:
        tool = "read_hash_fields"
        allowed = {"datasource_id": datasource, "key": "customers:1", "fields": ["phone"]}
        denied = {"datasource_id": datasource, "key": "customers:1", "fields": ["secret"]}
        masked, raw = "138****5678", "13812345678"
    else:
        raise RuntimeError("unknown native client kind")
    client = MCPClient(
        server_url=required("AGENTSQL_TEST_MCP_URL"),
        headers={"Authorization": f"Bearer {required('AGENTSQL_TEST_MCP_KEY')}",
                 "MCP-Protocol-Version": "2025-06-18"},
        timeout=30, sse_read_timeout=30,
    )
    try:
        client._initialize()
        if tool not in {item.name for item in client.list_tools()}:
            raise AssertionError(f"Dify did not discover {tool}")
        allow_audit = check(client.invoke_tool(tool, allowed), "allow", masked, raw)
        deny_audit = check(client.invoke_tool(tool, denied), "deny", masked, raw)
        print(json.dumps({"client": "Dify MCPClient 1.17.1", "kind": kind,
                          "allow_audit": allow_audit, "deny_audit": deny_audit}))
        print("V370_DIFY_NATIVE=PASS")
    finally:
        try:
            client.cleanup()
        except ValueError as error:
            print(f"DIFY_SESSION_CLEANUP_WARNING={type(error).__name__}")


if __name__ == "__main__":
    main()
