"""Run inside the pinned SQLBot derived image against an isolated demo gateway.

Requires AGENTSQL_SQLBOT_CONFIG and the configured Agent Key environment variable.
Uses actual SQLBot database entry points; no business database credentials needed.
"""
import json
import os
from pathlib import Path
from types import SimpleNamespace

import sqlbot_xpack  # Match upstream main.py initialization order.
from apps.db import db
from agentsql_sqlbot import AdapterError


def main():
    config_path = Path(os.environ['AGENTSQL_SQLBOT_CONFIG'])
    config = json.loads(config_path.read_text())
    binding = config['bindings'][0]
    ds = SimpleNamespace(oid=binding['workspace_id'], name=binding['datasource_name'], type='pg')
    assert db.check_connection(None, ds, is_raise=True) is True
    assert 'public' in db.get_schema(ds)
    tables = [t.tableName for t in db.get_tables(ds)]
    assert 'demo_b2_customers' in tables and 'internal_notes' not in tables, tables
    fields = db.get_fields(ds, 'demo_b2_customers')
    assert fields and all(f.fieldType for f in fields)
    sql = ('SELECT c.id, c.full_name, c.phone, c.email, c.region, o.status '
           'FROM public.demo_b2_customers c JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1')
    allowed = db.exec_sql(ds, sql)
    assert allowed['data'] and allowed['agentsql']['audit_id'] > 0
    row = allowed['data'][0]
    assert '*' in row['phone'] and '*' in row['email'], 'Sensitive demo values were not masked'
    assert isinstance(row['id'], int), 'Explicit numeric chart alias was not converted'
    denied_audit = None
    try:
        db.exec_sql(ds, 'SELECT id FROM public.internal_notes LIMIT 1')
        raise AssertionError('Unauthorized query was accepted')
    except AdapterError as error:
        assert 'audit_id=' in str(error)
        denied_audit = int(str(error).split('audit_id=')[1])
    for operation in [lambda: db.get_engine(ds), lambda: db.get_uri(ds)]:
        try:
            operation()
            raise AssertionError('A direct connector was reachable')
        except AdapterError:
            pass
    ds.oid += 1
    try:
        db.exec_sql(ds, sql)
        raise AssertionError('Cross-workspace mapping was accepted')
    except AdapterError:
        pass
    ds.oid -= 1
    key = os.environ[binding['key_env']]
    os.environ[binding['key_env']] = 'asql_invalid_test_key'
    try:
        db.exec_sql(ds, sql)
        raise AssertionError('Invalid Agent Key was accepted')
    except AdapterError:
        pass
    finally:
        os.environ[binding['key_env']] = key
    # A separate config endpoint exercises an unavailable gateway without stopping it.
    unavailable = dict(config, endpoint='http://127.0.0.1:1/mcp', timeout_seconds=1)
    failure_path = Path('/tmp/agentsql-unavailable.json')
    failure_path.write_text(json.dumps(unavailable))
    os.environ['AGENTSQL_SQLBOT_CONFIG'] = str(failure_path)
    try:
        db.exec_sql(ds, sql)
        raise AssertionError('Unavailable gateway was accepted')
    except AdapterError:
        pass
    finally:
        os.environ['AGENTSQL_SQLBOT_CONFIG'] = str(config_path)
    print(json.dumps({'result': 'SQLBOT_REAL_RUNTIME_PASS', 'tables': tables,
        'typed_fields': len(fields), 'allowed_audit_id': allowed['agentsql']['audit_id'],
        'denied_audit_id': denied_audit, 'masking': True, 'numeric_chart_alias': True,
        'direct_connectors_blocked': True, 'cross_workspace_blocked': True,
        'invalid_key_blocked': True, 'unavailable_gateway_blocked': True}))


if __name__ == '__main__':
    main()
