#!/usr/bin/env python3
"""Synthetic SQLite preservation/restore fixture; never accepts a live database."""
import argparse
from pathlib import Path
import sqlite3

METADATA = """
INSERT INTO agents(id,name,api_key_hash,level) VALUES('upgrade-agent','Synthetic upgrade reader','unusable-fixture-hash','readonly');
INSERT INTO datasources(id,name,db_type,host,port,database,username,password_enc) VALUES('upgrade-source','Synthetic source','postgres','invalid.example.test',5432,'fixture','synthetic_reader','opaque-encrypted-fixture');
INSERT INTO policies(id,agent_id,datasource_id,object_type,object_name,columns,action) VALUES('upgrade-policy','upgrade-agent','upgrade-source','table','public.synthetic','id,phone','select');
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo) VALUES('upgrade-mask','upgrade-source','synthetic','phone','phone','mask');
"""
AUDIT = "INSERT INTO audit_logs(agent_id,datasource_id,decision,rows_returned,mcp_tool) VALUES('upgrade-agent','upgrade-source','deny',0,'query');"
CHECKS = [
    ("SELECT name||'|'||api_key_hash||'|'||level FROM agents WHERE id='upgrade-agent'", 'Synthetic upgrade reader|unusable-fixture-hash|readonly'),
    ("SELECT host||'|'||password_enc FROM datasources WHERE id='upgrade-source'", 'invalid.example.test|opaque-encrypted-fixture'),
    ("SELECT object_name||'|'||columns||'|'||action FROM policies WHERE id='upgrade-policy'", 'public.synthetic|id,phone|select'),
    ("SELECT column_name||'|'||algo FROM mask_rules WHERE id='upgrade-mask'", 'phone|mask'),
    ("SELECT decision||'|'||rows_returned FROM audit_logs WHERE agent_id='upgrade-agent'", 'deny|0'),
]

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory',type=Path)
    parser.add_argument('operation',choices=['seed','check','mutate','restore'])
    args=parser.parse_args()
    root=args.directory.resolve()
    if root.name!='sqlite' or not root.parent.name.startswith('upgrade-v') or not (root/'isolated-fixture').is_file():
        raise SystemExit('refusing a directory without the isolated fixture marker')
    with sqlite3.connect(root/'metadata.db') as conn:
        if args.operation=='seed':
            conn.executescript(METADATA+AUDIT);conn.commit()
            with sqlite3.connect(root/'backup.db') as backup:conn.backup(backup)
        elif args.operation=='mutate':
            conn.execute("UPDATE agents SET name='temporary-v4-marker' WHERE id='upgrade-agent'");conn.commit()
        elif args.operation=='restore':
            with sqlite3.connect(root/'backup.db') as backup:backup.backup(conn)
        if args.operation in ('seed','check','restore'):
            assert conn.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
            for sql,expected in CHECKS:assert str(conn.execute(sql).fetchone()[0])==expected
            print('SQLITE_SCHEMA='+str(conn.execute('SELECT MAX(version) FROM schema_migrations').fetchone()[0]))
    print('SQLITE_'+args.operation.upper()+'=PASS')

if __name__=='__main__':main()
