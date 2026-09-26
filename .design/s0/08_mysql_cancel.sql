DROP DATABASE IF EXISTS s0_cancel;
CREATE DATABASE s0_cancel;
CREATE TABLE s0_cancel.t(id int PRIMARY KEY,v int);
INSERT INTO s0_cancel.t VALUES(1,0),(2,0);
CREATE PROCEDURE s0_cancel.p_stream()
BEGIN
  SELECT 1 AS first_result;
  SELECT id FROM s0_cancel.t WHERE SLEEP(30)=0;
END;
CREATE USER 'app'@'%' IDENTIFIED BY 'p';
CREATE USER 'kill_none'@'%' IDENTIFIED BY 'p';
CREATE USER 'kill_process'@'%' IDENTIFIED BY 'p';
CREATE USER 'kill_admin'@'%' IDENTIFIED BY 'p';
GRANT SELECT,UPDATE,EXECUTE ON s0_cancel.* TO 'app'@'%';
GRANT PROCESS ON *.* TO 'kill_process'@'%';
GRANT CONNECTION_ADMIN ON *.* TO 'kill_admin'@'%';

-- Target app connection:
SELECT CONNECTION_ID(); -- :target_id
START TRANSACTION;
UPDATE s0_cancel.t SET v=1 WHERE id=1;
SELECT id FROM s0_cancel.t WHERE SLEEP(30)=0;

-- A second physical connection is mandatory while target is busy.
-- kill_none and kill_process get 1095 for another user's thread.
-- kill_admin succeeds. A second connection authenticated as the same `app`
-- account also succeeds without PROCESS/CONNECTION_ADMIN.
KILL QUERY :target_id;

-- After target receives 1317:
SELECT @@in_transaction,CONNECTION_ID(); -- still 1, same connection
SELECT v FROM s0_cancel.t WHERE id=1;     -- target sees 1
-- independent observer sees 0 until target rolls back
ROLLBACK;
