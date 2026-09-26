-- Run on mysql:8 with:
-- --gtid-mode=ON --enforce-gtid-consistency=ON --log-bin=mysql-bin
-- --binlog-format=ROW --server-id=6204 --log-bin-trust-function-creators=ON

DROP DATABASE IF EXISTS s0_change;
CREATE DATABASE s0_change;
CREATE TABLE s0_change.t(id int PRIMARY KEY, v int);
CREATE VIEW s0_change.v AS SELECT id,v FROM s0_change.t;
CREATE TRIGGER s0_change.tr_bi BEFORE INSERT ON s0_change.t
  FOR EACH ROW SET NEW.v=coalesce(NEW.v,0);
CREATE PROCEDURE s0_change.p() SELECT 1;
CREATE EVENT s0_change.ev ON SCHEDULE EVERY 1 DAY DO SELECT 1;

-- Fpre waterline:
SHOW BINARY LOG STATUS;
SELECT @@GLOBAL.gtid_executed;
-- independent DDL connection:
CREATE VIEW s0_change.v_after_fpre AS SELECT id FROM s0_change.t;
-- Fpost target waterline:
SHOW BINARY LOG STATUS;
SELECT @@GLOBAL.gtid_executed;
-- watcher starts at Fpre File/Position and consumes until End_log_pos >= the
-- Fpost Position, accumulating every Gtid event into its durable seen set:
SHOW BINLOG EVENTS IN 'mysql-bin.000001' FROM 0 LIMIT 1000;
SELECT GTID_SUBSET(:fpost_gtid_set,:watcher_seen_gtid_set);

-- Pure SQL generation/epoch counterexample A: DDL implicitly commits. Simulate
-- gateway process death by closing this connection immediately after CREATE,
-- before issuing the epoch UPDATE.
CREATE TABLE s0_change.ddl_epoch(id int PRIMARY KEY, epoch bigint NOT NULL);
INSERT INTO s0_change.ddl_epoch VALUES(1,0);
START TRANSACTION;
CREATE VIEW s0_change.v_gap AS SELECT id FROM s0_change.t;
-- disconnect here
SELECT epoch FROM s0_change.ddl_epoch; -- 0, but v_gap exists

-- Counterexample B: bump commits before a failing DDL because DDL implicitly
-- commits even when the DDL later fails.
START TRANSACTION;
UPDATE s0_change.ddl_epoch SET epoch=2 WHERE id=1;
CREATE VIEW s0_change.v_gap AS SELECT v FROM s0_change.t; -- duplicate error
ROLLBACK;
SELECT epoch FROM s0_change.ddl_epoch; -- 2, although requested DDL failed
