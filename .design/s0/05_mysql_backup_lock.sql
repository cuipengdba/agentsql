DROP DATABASE IF EXISTS s0_backup;
CREATE DATABASE s0_backup;
CREATE TABLE s0_backup.t(id int PRIMARY KEY, v int);
CREATE TABLE s0_backup.t_trigger_new(id int PRIMARY KEY, v int);
CREATE VIEW s0_backup.v_replace AS SELECT id FROM s0_backup.t;
CREATE VIEW s0_backup.v_alter AS SELECT id FROM s0_backup.t;
CREATE VIEW s0_backup.v_drop AS SELECT id FROM s0_backup.t;
CREATE VIEW s0_backup.v_rename AS SELECT id FROM s0_backup.t;
CREATE PROCEDURE s0_backup.p_alter() SELECT 1;
CREATE PROCEDURE s0_backup.p_drop() SELECT 1;
CREATE FUNCTION s0_backup.f_alter() RETURNS INT DETERMINISTIC RETURN 1;
CREATE FUNCTION s0_backup.f_drop() RETURNS INT DETERMINISTIC RETURN 1;
CREATE TRIGGER s0_backup.tr_drop BEFORE INSERT ON s0_backup.t
  FOR EACH ROW SET NEW.v=coalesce(NEW.v,0);
CREATE EVENT s0_backup.ev_alter ON SCHEDULE EVERY 1 DAY DO SELECT 1;
CREATE EVENT s0_backup.ev_drop ON SCHEDULE EVERY 1 DAY DO SELECT 1;
INSTALL PLUGIN rewriter SONAME 'rewriter.so';
INSTALL COMPONENT 'file://component_validate_password';

-- Session L (keep open):
LOCK INSTANCE FOR BACKUP;

-- Session D:
SET SESSION lock_wait_timeout=1;
-- Execute each row from the matrix in spike.go on this independent connection.
-- ERROR 1205 means it waited on the backup lock. Other errors (syntax, missing
-- .so, already installed, etc.) prove the statement passed the backup-lock gate
-- but do not prove the DDL itself is valid.
-- The matrix includes CREATE/DROP UDF, INSTALL/UNINSTALL PLUGIN and
-- INSTALL/UNINSTALL COMPONENT; post-unlock controls distinguish backup waits
-- from missing-library/not-found errors.

-- Session L cleanup:
UNLOCK INSTANCE;
