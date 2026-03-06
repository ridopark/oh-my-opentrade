-- Allow zero volume for crypto bars (idle periods emit volume=0).
ALTER TABLE market_bars DROP CONSTRAINT IF EXISTS market_bars_volume_check;
ALTER TABLE market_bars ADD CONSTRAINT market_bars_volume_check CHECK (volume >= 0);
