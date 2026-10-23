"""Real SQLBot HTTP acceptance, run inside the isolated pinned SQLBot image.

This script uses SQLBot's normal encrypted login and public business APIs. It
does not replace the model, provide canned SQL responses, or open a business
database connection. Use synthetic data only. Credentials stay in environment
variables and are never written to its evidence report.
"""
from __future__ import annotations

import argparse
import asyncio
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


class SQLBot:
    def __init__(self, endpoint: str, account: str, password: str):
        self.endpoint = endpoint.rstrip("/") + "/api/v1"
        self.token = ""
        # Match the pinned application's initialization order.
        import sqlbot_xpack  # noqa: F401
        from common.utils.crypto import sqlbot_encrypt

        async def encrypted():
            return {"username": await sqlbot_encrypt(account),
                    "password": await sqlbot_encrypt(password)}

        login = self.request("POST", "/login/access-token", asyncio.run(encrypted()), form=True)
        self.token = login["access_token"]
        if not self.token:
            raise RuntimeError("SQLBot did not issue a login token")

    def request(self, method, path, body=None, *, form=False, stream=False, timeout=1200):
        headers = {"Accept-Language": "zh-CN"}
        if self.token:
            headers["X-SQLBOT-TOKEN"] = "Bearer " + self.token
        if body is not None:
            headers["Content-Type"] = ("application/x-www-form-urlencoded" if form else "application/json")
            data = (urllib.parse.urlencode(body) if form else json.dumps(body, ensure_ascii=False)).encode()
        else:
            data = None
        request = urllib.request.Request(self.endpoint + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                raw = response.read().decode()
        except urllib.error.HTTPError as error:
            # Responses can contain upstream configuration. Keep the public error
            # bounded and save only reviewed synthetic responses in test runs.
            raise RuntimeError(f"SQLBot {method} {path}: HTTP {error.code}") from None
        if stream:
            return [json.loads(line[5:]) for line in raw.splitlines() if line.startswith("data:")]
        parsed = json.loads(raw)
        if isinstance(parsed, dict) and "code" in parsed:
            if parsed["code"] != 0:
                raise RuntimeError(f"SQLBot {method} {path}: application failure")
            return parsed.get("data")
        return parsed

    def setup_model(self, endpoint, model):
        existing = self.request("GET", "/system/aimodel")
        match = next((m for m in existing if m["name"] == "AgentSQL local acceptance model"), None)
        if match:
            return match["id"]
        result = self.request("POST", "/system/aimodel", {
            "name": "AgentSQL local acceptance model", "model_type": 0, "base_model": model,
            "supplier": 0, "protocol": 1, "default_model": True,
            "api_domain": endpoint, "api_key": os.environ.get("SQLBOT_ACCEPTANCE_MODEL_KEY", "local-only"),
            "config_list": [{"key": "temperature", "val": "0", "name": "temperature"},
                            {"key": "max_tokens", "val": "2048", "name": "max_tokens"}],
        })
        return result["id"]

    def setup_users(self):
        workspaces = self.request("GET", "/system/workspace")
        existing_users = self.request("GET", "/user/pager/1/100")
        existing_users = existing_users.get("items", existing_users.get("data", []))
        result = []
        for suffix in ("a", "b"):
            name = f"AgentSQL acceptance workspace {suffix.upper()}"
            workspace = next((w for w in workspaces if w["name"] == name), None)
            if workspace is None:
                workspace = self.request("POST", "/system/workspace", {"name": name})
            account = f"agentsql_qa_{suffix}"
            user = next((u for u in existing_users if u["account"] == account), None)
            if user is None:
                user = self.request("POST", "/user", {
                    "account": account, "name": f"AgentSQL QA {suffix.upper()}",
                    "email": f"{account}@example.invalid", "status": 1, "origin": 0,
                    "oid": int(workspace["id"]), "oid_list": [int(workspace["id"])],
                })
            self.request("PUT", "/system/workspace/uws", {
                "oid": int(workspace["id"]), "uid": int(user["id"]), "weight": 1,
            })
            result.append({"account": account, "user_id": str(user["id"]),
                           "workspace_id": str(workspace["id"]), "workspace_name": name})
        return {"identities": result}

    def create_datasource(self, name):
        existing = self.request("GET", "/datasource/list")
        match = next((d for d in existing if d["name"] == name), None)
        if match:
            return {"datasource_id": match["id"], "name": match["name"], "workspace_id": match["oid"]}
        from apps.datasource.utils.utils import aes_encrypt
        # The adapter never reads these connection credentials. Valid structural
        # configuration is needed by SQLBot's own schema/prompt formatting.
        configuration = aes_encrypt(json.dumps({"host": "gateway-only.invalid", "port": 5432,
            "username": "no-direct-access", "password": "", "database": "agentsql_demo", "dbSchema": "public"})).decode()
        ds = self.request("POST", "/datasource/add", {"name": name, "description": "Synthetic AgentSQL acceptance data",
            "type": "pg", "configuration": configuration, "tables": [], "recommended_config": 0})
        return {"datasource_id": ds["id"], "name": ds["name"], "workspace_id": ds["oid"]}

    def ask(self, datasource, question):
        chat = self.request("POST", "/chat/start", {"datasource": datasource, "question": question, "origin": 0})
        started = time.monotonic()
        events = self.request("POST", "/chat/question", {"chat_id": chat["id"], "question": question}, stream=True)
        result = self.request("GET", f"/chat/{chat['id']}/with_data")
        records = [r for r in result.get("records", []) if r.get("question") == question]
        record = records[-1] if records else {}
        data = record.get("data") or {}
        fields = {item.get("name") for item in data.get("fields_info", []) if item.get("is_numeric")}
        try:
            chart = json.loads(record.get("chart") or "null")
        except (TypeError, ValueError):
            chart = None
        y_columns = ((chart or {}).get("axis") or {}).get("y") or []
        checks = {"finished": record.get("finish") is True, "no_error": not record.get("error"),
                  "generated_sql": bool(record.get("sql")),
                  "generated_chart": bool(chart and y_columns and any(item.get("value") in fields for item in y_columns)),
                  "result_data": bool(data.get("data")), "model_tokens_recorded": record.get("total_tokens", 0) > 0}
        return {"chat_id": chat["id"], "question": question, "elapsed_seconds": round(time.monotonic() - started, 3),
                "events": events, "chat": result, "checks": checks, "passed": all(checks.values())}

    def select_tables(self, datasource, selected):
        available = self.request("POST", f"/datasource/getTables/{datasource}")
        names = set(selected)
        tables = [{"table_name": t["tableName"], "table_comment": t.get("tableComment") or "",
                   "custom_comment": t.get("tableComment") or "", "checked": True}
                  for t in available if t["tableName"] in names]
        if {t["table_name"] for t in tables} != names:
            raise ValueError("Requested table is absent from the authorized schema")
        self.request("POST", f"/datasource/chooseTables/{datasource}", tables)
        return {"datasource_id": str(datasource), "available": available, "selected": tables}

    def adapter_action(self, operation, body, expected_status):
        # These adapter routes return bounded, sanitized feedback by contract.
        path = "/agentsql/" + operation
        if operation not in {"bindings", "diagnostics", "approvals/request", "approvals/status", "approvals/execute"}:
            raise ValueError("Unsupported adapter acceptance operation")
        request = urllib.request.Request(self.endpoint + path,
            method="GET" if operation == "bindings" else "POST",
            data=None if operation == "bindings" else json.dumps(body).encode(),
            headers={"X-SQLBOT-TOKEN": "Bearer " + self.token, "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                status, data = response.status, json.load(response)
        except urllib.error.HTTPError as error:
            status, data = error.code, json.load(error)
        if status != expected_status:
            raise AssertionError(f"Adapter {operation}: expected HTTP {expected_status}, got {status}: {data}")
        return {"operation": operation, "status": status, "data": data}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["setup-model", "setup-users", "create-datasource", "select-tables", "whoami", "ask", "adapter"])
    parser.add_argument("--url", default="http://127.0.0.1:8000")
    parser.add_argument("--account", default="admin")
    parser.add_argument("--password-env", default="DEFAULT_PWD")
    parser.add_argument("--model-url", default="http://agentsql-v310-llama:8080/v1")
    parser.add_argument("--model", default="agentsql-local-qwen")
    parser.add_argument("--datasource", type=int)
    parser.add_argument("--datasource-name", default="AgentSQL PostgreSQL")
    parser.add_argument("--question")
    parser.add_argument("--tables", nargs="+")
    parser.add_argument("--operation")
    parser.add_argument("--body", default="{}", help="Synthetic operation payload; never a credential")
    parser.add_argument("--expect-status", type=int, default=200)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    client = SQLBot(args.url, args.account, os.environ[args.password_env])
    if args.action == "setup-model":
        result = {"model_id": client.setup_model(args.model_url, args.model), "model_name": args.model}
    elif args.action == "setup-users":
        result = client.setup_users()
    elif args.action == "create-datasource":
        result = client.create_datasource(args.datasource_name)
    elif args.action == "whoami":
        user = client.request("GET", "/user/info")
        result = {key: user.get(key) for key in ("id", "account", "oid", "isAdmin", "weight")}
    elif args.action == "select-tables":
        if args.datasource is None or not args.tables:
            parser.error("select-tables requires --datasource and --tables")
        result = client.select_tables(args.datasource, args.tables)
    elif args.action == "adapter":
        result = client.adapter_action(args.operation, json.loads(args.body), args.expect_status)
    else:
        if args.datasource is None or not args.question:
            parser.error("ask requires --datasource and --question")
        result = client.ask(args.datasource, args.question)
    Path(args.output).write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    if args.action == "ask" and not result["passed"]:
        raise AssertionError("Real SQLBot question did not complete all SQL, data and chart stages; inspect saved evidence")
    print(json.dumps({"action": args.action, "output": args.output, "completed": True}))


if __name__ == "__main__":
    main()
