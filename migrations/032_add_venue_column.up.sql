-- Gap 10 (MFT crypto / SHARED-INFRA): add explicit execution venue columns
-- so perp and cross-venue strategies can distinguish the same logical pair
-- across venues (e.g. BTC/USD on Coinbase vs. Hyperliquid perp). Columns
-- are nullable; existing equity rows stay NULL and consumers resolve the
-- implicit venue via domain.DefaultVenue(AssetClass).

ALTER TABLE order_intents ADD COLUMN IF NOT EXISTS venue TEXT;
ALTER TABLE trades         ADD COLUMN IF NOT EXISTS venue TEXT;

CREATE INDEX IF NOT EXISTS idx_order_intents_venue
    ON order_intents(venue)
    WHERE venue IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_trades_venue
    ON trades(venue, time DESC)
    WHERE venue IS NOT NULL;
