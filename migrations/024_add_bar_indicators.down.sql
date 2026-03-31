-- 024_add_bar_indicators.down.sql

ALTER TABLE market_bars DROP COLUMN IF EXISTS avwaps;
ALTER TABLE market_bars DROP COLUMN IF EXISTS ema200;
ALTER TABLE market_bars DROP COLUMN IF EXISTS ema50;
ALTER TABLE market_bars DROP COLUMN IF EXISTS ema21;
ALTER TABLE market_bars DROP COLUMN IF EXISTS ema9;
