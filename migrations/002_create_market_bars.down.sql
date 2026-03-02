-- 002_create_market_bars.down.sql

-- Remove compression policy before dropping.
SELECT remove_compression_policy('market_bars', if_exists => TRUE);
DROP TABLE IF EXISTS market_bars;
