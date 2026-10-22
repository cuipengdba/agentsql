import ast
import hashlib
import json
import os
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import agentsql_sqlbot as adapter
import install


class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.ds = SimpleNamespace(oid=7, name="Reporting", type="pg")
        self.binding = {"workspace_id": 7, "datasource_name": "Reporting", "dialect": "pg",
                        "schema": "public", "datasource_id": "ds-report", "key_env": "TEST_AGENT_KEY"}
        self.config = {"version": 1, "endpoint": "https://gateway.example/mcp", "bindings": [self.binding]}

    def test_workspace_mapping_and_missing_config_fail_closed(self):
        with TemporaryDirectory() as directory:
            config = Path(directory)/"config.json"
            config.write_text(json.dumps(self.config))
            with patch.dict(os.environ, {"AGENTSQL_SQLBOT_CONFIG": str(config)}):
                self.assertEqual(adapter._binding(self.ds)[1]["datasource_id"], "ds-report")
                self.ds.oid = 8
                with self.assertRaises(adapter.AdapterError): adapter._binding(self.ds)
                self.ds.oid = 7
                self.config["bindings"].append(self.binding)
                config.write_text(json.dumps(self.config))
                with self.assertRaises(adapter.AdapterError): adapter._binding(self.ds)
                config.unlink()
                with self.assertRaises(adapter.AdapterError): adapter._binding(self.ds)

    def test_endpoint_and_missing_key(self):
        for url in ["http://remote.example/mcp", "https://user:secret@gateway.example/mcp",
                    "https://gateway.example/mcp?token=secret", "https://gateway.example/other"]:
            with self.assertRaises(adapter.AdapterError): adapter.Client({"endpoint": url}, self.binding)
        with patch.dict(os.environ, {"TEST_AGENT_KEY": ""}):
            with self.assertRaises(adapter.AdapterError): adapter.Client(self.config, self.binding)

    def test_result_requires_audit_rectangular_rows_and_unique_aliases(self):
        for data in [{"result": {"Columns": ["id"], "Rows": [["1"]]}},
                     {"audit_id": 1, "result": {"Columns": ["X", "x"], "Rows": [["1", "2"]]}},
                     {"audit_id": 1, "result": {"Columns": ["id"], "Rows": [["1", "2"]]}}]:
            with self.assertRaises(adapter.AdapterError): adapter._query_result(data, "select", False)

    def test_masked_and_precision_sensitive_values_are_not_reconstructed(self):
        data = {"audit_id": 21, "result": {"Columns": ["phone", "total", "huge"],
                "Rows": [["138****5678", "12.5", "9007199254740993"]]}}
        result = adapter._query_result(data, "SELECT phone,total,huge FROM public.report", False,
                                       ["phone", "total", "huge"])
        self.assertEqual(result["data"][0], {"phone": "138****5678", "total": 12.5, "huge": "9007199254740993"})
        self.assertEqual([f["is_numeric"] for f in result["fields_info"]], [False, True, False])
        self.assertEqual(data["result"]["Rows"][0][1], "12.5")

    def test_tool_rejects_nonallow_and_never_returns_raw_error(self):
        with patch.dict(os.environ, {"TEST_AGENT_KEY": "test-key-not-logged"}):
            client = adapter.Client(self.config, self.binding)
        for decision in ["deny", "warn", "approve", "error", "unknown"]:
            wire = {"content": [{"type": "text", "text": json.dumps({"decision": decision,
                    "reason": "private-sql-password", "data": {"audit_id": 42}})}]}
            with patch.object(client, "rpc", return_value=wire):
                with self.assertRaises(adapter.AdapterError) as caught: client.tool("query", {})
                self.assertNotIn("private-sql-password", str(caught.exception))
                self.assertIn("audit_id=42", str(caught.exception))

    def test_driver_entry_points_are_always_blocked(self):
        with self.assertRaises(adapter.AdapterError): adapter.deny_direct()

    def test_malformed_protocol_shapes_fail_closed(self):
        with patch.dict(os.environ, {"TEST_AGENT_KEY": "test-key-not-logged"}):
            client = adapter.Client(self.config, self.binding)
        values = [[], {"content": []}, {"content": [{"type": "text", "text": "[]"}]},
                  {"content": [{"type": "text", "text": '{"decision":"deny","data":[]}'}]}]
        for value in values:
            with self.subTest(value=value), patch.object(client, "rpc", return_value=value):
                with self.assertRaises(adapter.AdapterError): client.tool("query", {})

    def test_query_uses_exact_sql_without_fallback_or_retry(self):
        sql = "SELECT id FROM public.report LIMIT 1;"
        calls = []
        class FakeClient:
            def __init__(self, *args): pass
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def tool(self, name, arguments):
                calls.append((name, arguments))
                if name == "list_datasources": return [{"id": "ds-report", "db_type": "postgres"}]
                raise adapter.AdapterError("gateway unavailable")
        with patch.object(adapter, "_binding", return_value=(self.config, self.binding)), patch.object(adapter, "Client", FakeClient):
            with self.assertRaises(adapter.AdapterError): adapter.dispatch("exec_sql", self.ds, sql=sql)
        self.assertEqual(calls, [("list_datasources", {}), ("query", {"datasource_id": "ds-report", "sql": sql})])

    def test_schema_is_filtered_and_type_absence_blocks(self):
        class FakeClient:
            def __init__(self, *args): pass
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def tool(self, name, args):
                if name == "list_datasources": return [{"id": "ds-report", "db_type": "postgres"}]
                return [{"schema": "public", "table": "report", "column": "id", "data_type": "bigint"},
                        {"schema": "private", "table": "secrets", "column": "key", "data_type": "text"}]
        with patch.object(adapter, "_binding", return_value=(self.config, self.binding)), patch.object(adapter, "Client", FakeClient):
            result = adapter.dispatch("get_fields", self.ds, table_name="report")
            self.assertEqual([(f.fieldName, f.fieldType) for f in result], [("id", "bigint")])
            with self.assertRaises(adapter.AdapterError): adapter.dispatch("get_fields", self.ds, table_name="report;drop")


class InstallerTests(unittest.TestCase):
    def test_source_drift_is_rejected(self):
        with self.assertRaises(ValueError): install.patch("def exec_sql(): pass\n")

    def test_patched_routes_precede_all_original_driver_calls(self):
        fixture = "\n".join(f"def {name}(ds=None, sql=None, origin_column=False, table_name=None, is_raise=False):\n    direct_database_call()\n"
                              for name in sorted(set(install.ROUTES) | install.BLOCKED))
        with patch.object(install, "SOURCE_SHA256", hashlib.sha256(fixture.encode()).hexdigest()):
            updated = install.patch(fixture)
        tree = ast.parse(updated)
        functions = [n for n in tree.body if isinstance(n, ast.FunctionDef)]
        self.assertEqual(len(functions), 13)
        for function in functions:
            self.assertIsInstance(function.body[0], ast.Return)
            expected = "_agentsql_dispatch" if function.name in install.ROUTES else "_agentsql_deny_direct"
            self.assertEqual(function.body[0].value.func.id, expected)
        with self.assertRaises(ValueError): install.patch(updated)


if __name__ == "__main__": unittest.main()
