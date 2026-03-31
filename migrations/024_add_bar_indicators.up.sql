-- 024_add_bar_indicators.up.sql
-- Add EMA and AVWAP indicator columns to market_bars for enriched bar data.

ALTER TABLE market_bars ADD COLUMN IF NOT EXISTS ema9   DOUBLE PRECISION;
ALTER TABLE market_bars ADD COLUMN IF NOT EXISTS ema21  DOUBLE PRECISION;
ALTER TABLE market_bars ADD COLUMN IF NOT EXISTS ema50  DOUBLE PRECISION;
ALTER TABLE market_bars ADD COLUMN IF NOT EXISTS ema200 DOUBLE PRECISION;
ALTER TABLE market_bars ADD COLUMN IF NOT EXISTS avwaps JSONB;
