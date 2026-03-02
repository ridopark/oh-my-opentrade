-- 004_create_thought_logs.down.sql

SELECT remove_compression_policy('thought_logs', if_exists => TRUE);
DROP TABLE IF EXISTS thought_logs;
