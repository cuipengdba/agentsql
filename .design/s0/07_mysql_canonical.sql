-- CONCAT_WS is not an injective tuple encoding.
SELECT CONCAT_WS('|','a|b','c') lhs, CONCAT_WS('|','a','b|c') rhs,
       CONCAT_WS('|','a|b','c')=CONCAT_WS('|','a','b|c') collision;
SELECT CONCAT_WS('|','a',NULL,'b') lhs, CONCAT_WS('|','a','b',NULL) rhs,
       CONCAT_WS('|','a',NULL,'b')=CONCAT_WS('|','a','b',NULL) collision;
SELECT CONCAT_WS('|','expr:x|y','中文😀') lhs,
       CONCAT_WS('|','expr:x','y|中文😀') rhs,
       CONCAT_WS('|','expr:x|y','中文😀')=CONCAT_WS('|','expr:x','y|中文😀') collision;

-- The runner first attempts 64 Chinese characters (the SQL identifier character
-- limit; on the Linux/InnoDB image filename encoding fails with 1030), then
-- creates a 30-Chinese-character identifier. The latter is 90 UTF-8 bytes, so
-- insertion into VARBINARY(64) fails with error 1406 in strict mode.
CREATE TEMPORARY TABLE identifier_key(name VARBINARY(64));

-- Case probe, repeated in fresh instances initialized with l_c_t_n 0 and 1.
CREATE DATABASE `CaseDb`;
CREATE TABLE `CaseDb`.`CaseTbl`(id int);
SELECT @@lower_case_table_names;
SELECT TABLE_SCHEMA,TABLE_NAME FROM information_schema.TABLES
WHERE lower(TABLE_SCHEMA)='casedb' AND lower(TABLE_NAME)='casetbl';
