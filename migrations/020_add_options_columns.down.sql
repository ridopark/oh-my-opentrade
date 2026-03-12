ALTER TABLE trades
  DROP COLUMN IF EXISTS instrument_type,
  DROP COLUMN IF EXISTS option_symbol,
  DROP COLUMN IF EXISTS underlying,
  DROP COLUMN IF EXISTS strike,
  DROP COLUMN IF EXISTS expiry,
  DROP COLUMN IF EXISTS option_right,
  DROP COLUMN IF EXISTS premium,
  DROP COLUMN IF EXISTS delta_at_entry,
  DROP COLUMN IF EXISTS iv_at_entry;

ALTER TABLE orders
  DROP COLUMN IF EXISTS instrument_type,
  DROP COLUMN IF EXISTS option_symbol,
  DROP COLUMN IF EXISTS underlying,
  DROP COLUMN IF EXISTS strike,
  DROP COLUMN IF EXISTS expiry,
  DROP COLUMN IF EXISTS option_right;
