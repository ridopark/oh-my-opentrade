-- 005_create_strategy_dna_history.down.sql

SELECT remove_compression_policy('strategy_dna_history', if_exists => TRUE);
DROP TABLE IF EXISTS strategy_dna_history;
