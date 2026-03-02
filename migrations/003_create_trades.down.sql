-- 003_create_trades.down.sql

SELECT remove_compression_policy('trades', if_exists => TRUE);
DROP TABLE IF EXISTS trades;
