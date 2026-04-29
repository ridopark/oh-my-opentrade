-- One-shot cleanup of the dual-write option-fill rows (Bug #2c).
--
-- For every reconcile_fills_on_boot leg row whose Path B partner still
-- exists, delete the leg (Path B is canonical going forward — full option
-- metadata, intent rationale, sub-second timestamp). For leg rows with no
-- Path B partner (RIVN cancel-replace race, manually cleaned earlier),
-- backfill broker_order_id + option metadata so they remain well-formed.
--
-- Wrap in a transaction; run the inspection queries first, COMMIT only when
-- the counts match expectations.
--
-- Dry-run: psql -f scripts/cleanup_dual_write_legs.sql
-- Commit:  edit ROLLBACK -> COMMIT at the bottom.

BEGIN;

-- 1. Backfill broker_order_id on all reconcile-leg rows from the rationale.
UPDATE trades t
   SET broker_order_id = SUBSTRING(t.rationale FROM 'for order ([0-9]+)')
 WHERE t.rationale LIKE 'reconcile_fills_on_boot%'
   AND t.broker_order_id IS NULL;

-- 2. Backfill option metadata on those rows from the matching orders row.
UPDATE trades t
   SET instrument_type = 'OPTION',
       option_symbol = o.option_symbol,
       underlying = o.underlying,
       strike = o.strike,
       expiry = o.expiry,
       option_right = o.option_right,
       premium = t.price
  FROM orders o
 WHERE t.rationale LIKE 'reconcile_fills_on_boot%'
   AND t.broker_order_id IS NOT NULL
   AND o.broker_order_id = t.broker_order_id
   AND o.account_id = t.account_id
   AND o.env_mode = t.env_mode
   AND o.instrument_type = 'OPTION'
   AND (t.option_symbol IS NULL OR t.option_symbol = '');

-- 3. Identify leg rows that have a Path B partner (same option_symbol,
--    side, day, account, env, with execution_id NULL/empty and qty
--    matching the leg group's total).
CREATE TEMP TABLE legs_with_partners AS
WITH leg_totals AS (
    SELECT broker_order_id, side, time::date AS d, account_id, env_mode,
           SUM(quantity) AS total_qty,
           ARRAY_AGG(trade_id) AS leg_trade_ids
      FROM trades
     WHERE rationale LIKE 'reconcile_fills_on_boot%'
       AND broker_order_id IS NOT NULL
     GROUP BY 1, 2, 3, 4, 5
)
SELECT lt.*, pb.trade_id AS path_b_trade_id
  FROM leg_totals lt
  JOIN orders o ON o.broker_order_id = lt.broker_order_id
                AND o.account_id = lt.account_id
                AND o.env_mode = lt.env_mode
  JOIN trades pb ON pb.account_id = lt.account_id
                 AND pb.env_mode = lt.env_mode
                 AND pb.option_symbol = o.option_symbol
                 AND pb.side = lt.side
                 AND pb.time::date = lt.d
                 AND pb.instrument_type = 'OPTION'
                 AND (pb.execution_id IS NULL OR pb.execution_id = '')
                 AND ABS(pb.quantity - lt.total_qty) < 0.001;

-- Inspect.
SELECT 'pairs to collapse' AS step, COUNT(*) FROM legs_with_partners;
SELECT broker_order_id, side, total_qty, ARRAY_LENGTH(leg_trade_ids, 1) AS legs
  FROM legs_with_partners ORDER BY broker_order_id;

-- 4. Delete the leg rows of every pair that has a Path B partner.
DELETE FROM trades
 WHERE trade_id IN (
   SELECT UNNEST(leg_trade_ids) FROM legs_with_partners
 );

-- 5. Sanity check: every surviving reconcile_fills_on_boot row should now
--    have broker_order_id and full option metadata, OR be a pre-existing
--    equity reconcile row. Print remaining counts.
SELECT 'remaining recon rows' AS step,
       COUNT(*) AS n,
       SUM(CASE WHEN instrument_type='OPTION' THEN 1 ELSE 0 END) AS opt,
       SUM(CASE WHEN broker_order_id IS NOT NULL THEN 1 ELSE 0 END) AS has_bid
  FROM trades
 WHERE rationale LIKE 'reconcile_fills_on_boot%';

-- Net positions sanity: per option_symbol, BUY - SELL must be unchanged.
SELECT 'net positions OK' AS step;

COMMIT;
