import json
import hashlib
import os
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import urllib.error

import agentsql_sqlbot as adapter
import agentsql_sqlbot_context as context
import install


class ManagedTests(unittest.TestCase):
    def setUp(self):
        self.user = SimpleNamespace(id=23, oid=7, status=1, language="en")
        self.ds = SimpleNamespace(oid=7, name="Reporting", type="pg")
        self.binding = {"id": "binding-a", "workspace_id": 7, "datasource_name": "Reporting", "sqlbot_user_id": "23",
                        "agent_key_env": "TEST_AGENT_KEY", "connector_token_env": "TEST_CONNECTOR_TOKEN"}
        self.config = {"version": 2, "endpoint": "https://gateway.example/mcp", "bindings": [self.binding]}
        self.resolved = dict(self.binding, dialect="pg", schema="public", datasource_id="source-a",
                             agent_id="agent-a", key_env="TEST_AGENT_KEY", user_token="short-lived-token", revision=1)

    def test_mapping_requires_verified_exact_workspace_and_user(self):
        with patch.object(adapter, "_read_config", return_value=self.config), patch.object(adapter, "_resolve", return_value=self.resolved) as resolve:
            with patch.object(context, "authenticated_request_user", side_effect=adapter.AdapterError("no request")):
                with self.assertRaises(adapter.AdapterError): adapter._binding(self.ds)
            self.assertEqual(resolve.call_count, 0)
            with context.verified_scope(self.user):
                adapter._binding(self.ds)
                self.ds.oid = 8
                with self.assertRaises(adapter.AdapterError): adapter._binding(self.ds)
                self.ds.oid = 7
                self.user.id = 24
                with self.assertRaises(adapter.AdapterError): adapter._binding(self.ds)
            self.assertEqual(resolve.call_count, 1)

    def test_each_user_selects_own_binding_and_resolves_every_operation(self):
        second = dict(self.binding, id="binding-b", sqlbot_user_id="24")
        self.config["bindings"].append(second)
        def resolve(config, binding, user): return dict(self.resolved, id=binding["id"])
        with patch.object(adapter, "_read_config", return_value=self.config), patch.object(adapter, "_resolve", side_effect=resolve) as resolve:
            with context.verified_scope(self.user):
                self.assertEqual(adapter._binding(self.ds)[1]["id"], "binding-a")
                self.assertEqual(adapter._binding(self.ds)[1]["id"], "binding-a")
                self.user.id = 24
                self.assertEqual(adapter._binding(self.ds)[1]["id"], "binding-b")
            self.assertEqual(resolve.call_count, 3)

    def test_resolver_credential_and_identity_never_use_agent_key_as_connector(self):
        data = {"binding_id": "binding-a", "revision": 2, "agent_id": "agent-a", "datasource_id": "source-a",
                "schema": "public", "numeric_columns": ["total"], "user_token": "scoped-token", "expires_at": "2099-01-01T00:00:00Z"}
        calls = []
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def read(self, count): return json.dumps({"code": 0, "data": data}).encode()
        class Opener:
            def open(self, request, timeout): calls.append(request); return Response()
        with patch.object(adapter, "_transport", return_value=(self.config["endpoint"], Opener(), 5)), patch.dict(os.environ, {"TEST_AGENT_KEY": "agent-secret", "TEST_CONNECTOR_TOKEN": "connector-secret"}):
            result = adapter._resolve(self.config, self.binding, self.user)
            self.assertEqual(result["user_token"], "scoped-token")
            self.assertEqual(calls[0].full_url, "https://gateway.example/api/v1/integrations/sqlbot/resolve")
            self.assertEqual(calls[0].headers["Authorization"], "Bearer connector-secret")
            self.assertEqual(json.loads(calls[0].data), {"binding_id": "binding-a", "workspace_id": "7", "datasource_name": "Reporting", "sqlbot_user_id": "23"})
            self.assertNotIn("agent-secret", calls[0].data.decode())
            data["binding_id"] = "other-binding"
            with self.assertRaises(adapter.AdapterError): adapter._resolve(self.config, self.binding, self.user)

    def test_disabled_or_unavailable_binding_is_not_retried(self):
        class Opener:
            count = 0
            def open(self, *args, **kwargs):
                self.count += 1
                raise urllib.error.HTTPError("https://secret", 403, "private diagnostic", {}, None)
        opener = Opener()
        with patch.object(adapter, "_transport", return_value=(self.config["endpoint"], opener, 5)), patch.dict(os.environ, {"TEST_AGENT_KEY": "agent-secret", "TEST_CONNECTOR_TOKEN": "connector-secret"}):
            with self.assertRaises(adapter.AdapterError) as caught: adapter._resolve(self.config, self.binding, self.user)
            self.assertEqual(caught.exception.code, "binding_rejected")
            self.assertNotIn("private", str(caught.exception))
            self.assertNotIn("secret", str(caught.exception))
        self.assertEqual(opener.count, 1)

    def test_snowflake_ids_remain_exact_decimal_strings(self):
        identifier = 7518977644151443456
        self.user.oid = self.ds.oid = identifier
        self.user.id = identifier + 1
        self.binding["workspace_id"] = str(identifier)
        self.binding["sqlbot_user_id"] = str(identifier + 1)
        with patch.object(adapter, "_read_config", return_value=self.config), patch.object(adapter, "_resolve", return_value=self.resolved):
            with context.verified_scope(self.user):
                self.assertEqual(adapter._binding(self.ds)[1]["id"], "binding-a")
        for invalid in [float(identifier), True, "01", "1e5", "1.0", -1, 2**63]:
            with self.assertRaises(adapter.AdapterError): adapter._sqlbot_id(invalid)

    def test_mcp_always_transmits_verified_user_token(self):
        requests = []
        class Response:
            headers = {"Content-Type": "application/json"}
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def read(self, count): return b'{}'
        with patch.dict(os.environ, {"TEST_AGENT_KEY": "agent-key"}): client = adapter.Client(self.config, self.resolved)
        client.interval = 0
        with patch.object(client.opener, "open", side_effect=lambda request, **kwargs: (requests.append(request), Response())[1]):
            client._http({"method": "initialize"})
            client._http({"method": "tools/call"})
            client._http(method="DELETE")
        self.assertEqual([r.get_header("X-agentsql-user-authorization") for r in requests], ["Bearer short-lived-token"] * 3)

    def test_approval_request_accepts_only_audited_pending_and_safe_feedback(self):
        with patch.dict(os.environ, {"TEST_AGENT_KEY": "agent-key"}): client = adapter.Client(self.config, self.resolved)
        envelope = {"decision": "approve", "reason": "do not expose secret SQL", "data": {"approval_id": "ap-test", "audit_id": 17, "hits": [{"rule_id": "R004"}]}}
        wire = {"content": [{"type": "text", "text": json.dumps(envelope)}]}
        with patch.object(client, "rpc", return_value=wire):
            self.assertEqual(client.tool("request_approval", {})["status"], "pending")
            with self.assertRaises(adapter.AdapterError) as caught: client.tool("query", {})
            feedback = caught.exception.feedback()
            self.assertEqual(feedback["approval_id"], "ap-test")
            self.assertEqual(feedback["rules"], ["R004"])
            self.assertFalse(feedback["retryable"])
            self.assertNotIn("secret SQL", json.dumps(feedback))
        envelope["data"]["audit_id"] = 0
        with patch.object(client, "rpc", return_value={"content": [{"type": "text", "text": json.dumps(envelope)}]}):
            with self.assertRaises(adapter.AdapterError): client.tool("request_approval", {})

    def test_execute_approved_sends_only_sealed_id_once(self):
        calls = []
        class Client:
            def __init__(self, *args): pass
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def tool(self, name, args):
                calls.append((name, args))
                if name == "list_datasources": return [{"id": "source-a", "db_type": "postgres"}]
                return {"audit_id": 30, "result": {"Columns": ["phone"], "Rows": [["138****5678"]]}}
        with patch.object(adapter, "_binding", return_value=(self.config, self.resolved)), patch.object(adapter, "Client", Client):
            result = adapter.dispatch("execute_approved", self.ds, sql="SELECT forbidden", approval_id="ap-test")
        self.assertEqual(calls[-1], ("execute_approved", {"approval_id": "ap-test"}))
        self.assertEqual(result["data"][0]["phone"], "138****5678")
        self.assertEqual(result["sql"], "")


class ContextTests(unittest.TestCase):
    def test_embedded_assistant_and_ask_tokens_are_rejected(self):
        user = SimpleNamespace(id=1, oid=1, status=1)
        settings = SimpleNamespace(TOKEN_KEY="X-SQLBOT-TOKEN", ASSISTANT_TOKEN_KEY="X-SQLBOT-ASSISTANT-TOKEN")
        request = SimpleNamespace(headers={"X-SQLBOT-TOKEN": "Bearer valid"}, state=SimpleNamespace(current_user=user))
        with patch.dict(sys.modules, {"common.core.config": SimpleNamespace(settings=settings)}):
            self.assertEqual(context.authenticated_request_user(request).id, 1)
            for header in ["X-SQLBOT-ASK-TOKEN", "X-SQLBOT-ASSISTANT-TOKEN"]:
                request.headers[header] = "Embedded forged"
                with self.assertRaises(adapter.AdapterError): context.authenticated_request_user(request)
                del request.headers[header]
            request.headers.clear()
            with self.assertRaises(adapter.AdapterError): context.authenticated_request_user(request)

    def test_worker_carries_identity_and_resets_context(self):
        user = SimpleNamespace(id=23, oid=7, status=1)
        class Service:
            def __init__(self, session, current_user, question, current_assistant=None):
                self.current_user = current_user
            def run_task(self): yield context.current_identity().id
            def run_recommend_questions_task(self): yield context.current_identity().id
            def run_analysis_or_predict_task(self): yield context.current_identity().id
            def execute_sql(self, sql): raise AssertionError("old execute must not run")
        context.protect_llm_service(Service)
        with patch.object(context, "_managed", return_value=True), patch.object(context, "authenticated_request_user", return_value=user):
            service = Service(None, user, None)
        output = []
        def thread():
            with patch.object(context, "_managed", return_value=True), patch.object(context, "authenticated_request_user", side_effect=adapter.AdapterError("no context")):
                output.extend(service.run_task())
                with self.assertRaises(adapter.AdapterError): context.current_identity()
        with ThreadPoolExecutor(max_workers=1) as workers:
            workers.submit(thread).result(timeout=10)
        self.assertEqual(output, [23])

    def test_worker_governance_error_is_not_sql_retry_error(self):
        class SingleMessageError(Exception): pass
        class Service:
            def __init__(self, *args): pass
            def run_task(self): yield None
            def run_recommend_questions_task(self): yield None
            def run_analysis_or_predict_task(self): yield None
        context.protect_llm_service(Service)
        with patch.object(context, "_managed", return_value=False): instance = Service()
        instance.ds = object()
        with patch.dict(sys.modules, {"common.error": SimpleNamespace(SingleMessageError=SingleMessageError)}), patch.object(context, "dispatch", side_effect=adapter.AdapterError("Denied; audit_id=5")) as execute:
            with self.assertRaises(SingleMessageError): instance.execute_sql("select id")
        self.assertEqual(execute.call_count, 1)

    def test_cached_schema_is_intersection_of_gateway_and_sqlbot_fields(self):
        obj = SimpleNamespace(schema="public", table=SimpleNamespace(table_name="customers"),
                              fields=[SimpleNamespace(field_name="id"), SimpleNamespace(field_name="secret")])
        unknown = SimpleNamespace(schema="private", table=SimpleNamespace(table_name="passwords"), fields=[SimpleNamespace(field_name="secret")])
        with patch.object(context, "_managed", return_value=True), patch.object(context, "dispatch", return_value=[{"schema": "public", "table": "customers", "column": "id"}]):
            values = context.filter_table_objects([obj, unknown], object())
        self.assertEqual(len(values), 1)
        self.assertEqual([f.field_name for f in values[0].fields], ["id"])


class ManagedInstallerTests(unittest.TestCase):
    def test_agent_routes_register_before_upstream_catch_all_mount(self):
        source = ("app.include_router(api_router, prefix=settings.API_V1_STR)\n"
                  "mcp.setup_server()\n"
                  "sqlbot_xpack.init_fastapi_app(app)\n")
        with patch.dict(install.UPSTREAM, {"main.py": hashlib.sha256(source.encode()).hexdigest()}):
            patched = install.patch_support("main.py", source)
        self.assertLess(patched.index("app.include_router(_agentsql_router"),
                        patched.index("sqlbot_xpack.init_fastapi_app(app)"))
        self.assertLess(patched.index("app.include_router(_agentsql_router"),
                        patched.index("mcp.setup_server()"))
        drift = ("app.include_router(api_router, prefix=settings.API_V1_STR)\n"
                 "sqlbot_xpack.init_fastapi_app(app)\n"
                 "mcp.setup_server()\n")
        with patch.dict(install.UPSTREAM, {"main.py": hashlib.sha256(drift.encode()).hexdigest()}):
            with self.assertRaises(ValueError):
                install.patch_support("main.py", drift)

    def test_all_additional_upstream_files_are_hash_pinned(self):
        for name in ("apps/chat/task/llm.py", "apps/datasource/crud/datasource.py", "main.py"):
            with self.assertRaises(ValueError): install.patch_support(name, "unreviewed upstream source")

    def test_install_manifest_detects_source_and_adapter_drift(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            files = {}
            for name in set(install.UPSTREAM) | set(install.SUPPORT):
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(b"verified bytes")
                files[name] = install.hashlib.sha256(target.read_bytes()).hexdigest()
            (root / "agentsql-sqlbot-manifest.json").write_text(json.dumps({"adapter_version": 2, "upstream_version": "v1.10.2", "files": files}))
            self.assertEqual(install.verify_install(root)["result"], "PASS")
            (root / "main.py").write_bytes(b"modified")
            with self.assertRaises(ValueError): install.verify_install(root)


if __name__ == "__main__": unittest.main()
