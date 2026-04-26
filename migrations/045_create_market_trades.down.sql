-- 045_create_market_trades.down.sql
SELECT remove_retention_policy('market_trades', if_exists => TRUE);
SELECT remove_compression_policy('market_trades', if_exists => TRUE);
DROP TABLE IF EXISTS market_trades;
