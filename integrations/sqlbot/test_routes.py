"""Run in the pinned SQLBot runtime; stdlib-only jobs skip the FastAPI cases."""
import unittest
from types import SimpleNamespace
from unittest.mock import patch

try:
    from fastapi import FastAPI
    from fastapi.testclient import TestClient
    import agentsql_sqlbot_routes as routes
except ImportError:
    routes = None


@unittest.skipIf(routes is None, "requires the pinned SQLBot FastAPI runtime")
class ApprovalRouteTests(unittest.TestCase):
    def test_pinned_application_routes_precede_frontend_mount(self):
        from main import app
        routes_in_order = list(app.routes)
        binding_index = next(i for i, route in enumerate(routes_in_order)
                             if route.path == "/api/v1/agentsql/bindings")
        frontend_index = next(i for i, route in enumerate(routes_in_order)
                              if type(route).__name__ == "Mount" and route.path == "")
        self.assertLess(binding_index, frontend_index)

    def setUp(self):
        self.user = SimpleNamespace(id=7518977644151443457, oid=7518977644151443456, status=1)
        self.binding = {"id": "binding-a", "workspace_id": str(self.user.oid), "datasource_name": "Reporting", "sqlbot_user_id": str(self.user.id)}
        app = FastAPI()
        app.include_router(routes.router, prefix="/api/v1")
        self.client = TestClient(app)
        self.auth = patch.object(routes, "authenticated_request_user", return_value=self.user)
        self.config = patch.object(routes, "_read_config", return_value={"version": 2, "bindings": [self.binding]})
        self.auth.start(); self.config.start()
        self.addCleanup(self.auth.stop); self.addCleanup(self.config.stop)

    def test_list_preserves_snowflakes_and_filters_other_users(self):
        response = self.client.get("/api/v1/agentsql/bindings")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["bindings"][0]["workspace_id"], "7518977644151443456")
        self.binding["sqlbot_user_id"] = "7518977644151443458"
        self.assertEqual(self.client.get("/api/v1/agentsql/bindings").json()["bindings"], [])

    def test_mutations_require_post_explicit_confirmation_and_json(self):
        with patch.object(routes, "dispatch") as dispatch:
            self.assertEqual(self.client.get("/api/v1/agentsql/approvals/execute").status_code, 405)
            self.assertEqual(self.client.post("/api/v1/agentsql/approvals/execute", json={"binding_id": "binding-a", "approval_id": "ap-test"}).status_code, 400)
            self.assertEqual(self.client.post("/api/v1/agentsql/approvals/execute", content="binding_id=binding-a").status_code, 400)
            self.assertEqual(dispatch.call_count, 0)

    def test_cross_origin_and_unmapped_binding_never_dispatch(self):
        with patch.object(routes, "dispatch") as dispatch:
            body = {"binding_id": "binding-a", "approval_id": "ap-test", "confirmed": True}
            self.assertEqual(self.client.post("/api/v1/agentsql/approvals/execute", json=body, headers={"Origin": "https://evil.example"}).status_code, 400)
            body["binding_id"] = "other-binding"
            self.assertEqual(self.client.post("/api/v1/agentsql/approvals/execute", json=body).status_code, 400)
            self.assertEqual(dispatch.call_count, 0)

    def test_sealed_approval_executes_once_with_scoped_identity(self):
        from agentsql_sqlbot_context import current_identity
        calls = []
        def execute(operation, ds, **kwargs):
            calls.append((operation, ds.name, current_identity().id, kwargs))
            return {"agentsql": {"audit_id": 31}, "data": [{"phone": "138****5678"}]}
        with patch.object(routes, "dispatch", side_effect=execute):
            response = self.client.post("/api/v1/agentsql/approvals/execute", json={"binding_id": "binding-a", "approval_id": "ap-test", "confirmed": True})
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.headers["cache-control"], "no-store")
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][:3], ("execute_approved", "Reporting", self.user.id))
        self.assertEqual(calls[0][3]["approval_id"], "ap-test")
        self.assertIsNone(calls[0][3]["sql"])

    def test_request_seals_exact_sql_on_explicit_action(self):
        sql = "SELECT c.id FROM public.customers c WHERE c.id=1"
        with patch.object(routes, "dispatch", return_value={"approval_id": "ap-test", "audit_id": 32, "status": "pending"}) as dispatch:
            response = self.client.post("/api/v1/agentsql/approvals/request", json={"binding_id": "binding-a", "sql": sql, "reason": "Report", "confirmed": True})
        self.assertEqual(response.status_code, 200)
        self.assertEqual(dispatch.call_args.kwargs["sql"], sql)
        self.assertEqual(dispatch.call_args.args[0], "request_approval")


if __name__ == "__main__": unittest.main()
