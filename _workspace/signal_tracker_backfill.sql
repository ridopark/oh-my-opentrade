-- Backfill rows mistagged kind='entry' that were actually exits.
-- Bug: signal_tracker.getOrDeriveCorr returned the cached entry corr for
-- exit OrderIntent/Fill events, ignoring direction=CLOSE_LONG/CLOSE_SHORT.
-- Fixed in code; this corrects historical rows.
--
-- Strategy:
--   * validated/rejected rows: side came from cached scope (entry's side),
--     so we rewrite both kind AND side from payload->>'direction'.
--   * executed rows: side came from the actual broker fill payload, so it
--     reflects what physically happened — only rewrite kind.
--
-- Always run inside a transaction. Verify counts before COMMIT.

BEGIN;

-- Sanity counts before update.
SELECT 'before:validated_rejected' AS phase,
       payload->>'direction' AS direction, kind, side, status, COUNT(*)
FROM strategy_signal_events
WHERE kind = 'entry'
  AND status IN ('validated', 'rejected')
  AND payload->>'direction' IN ('CLOSE_LONG', 'CLOSE_SHORT')
GROUP BY 2, 3, 4, 5
ORDER BY 2, 3, 4, 5;

SELECT 'before:executed' AS phase,
       payload->>'direction' AS direction, kind, side, status, COUNT(*)
FROM strategy_signal_events
WHERE kind = 'entry'
  AND status = 'executed'
  AND payload->>'direction' IN ('CLOSE_LONG', 'CLOSE_SHORT')
GROUP BY 2, 3, 4, 5
ORDER BY 2, 3, 4, 5;

-- Fix validated/rejected: rewrite kind and side from direction.
UPDATE strategy_signal_events
SET kind = 'exit',
    side = CASE payload->>'direction'
             WHEN 'CLOSE_LONG'  THEN 'SELL'
             WHEN 'CLOSE_SHORT' THEN 'BUY'
           END
WHERE kind = 'entry'
  AND status IN ('validated', 'rejected')
  AND payload->>'direction' IN ('CLOSE_LONG', 'CLOSE_SHORT');

-- Fix executed: rewrite kind only; preserve actual fill side.
UPDATE strategy_signal_events
SET kind = 'exit'
WHERE kind = 'entry'
  AND status = 'executed'
  AND payload->>'direction' IN ('CLOSE_LONG', 'CLOSE_SHORT');

-- Verify nothing remains mistagged.
SELECT 'after:remaining_buggy' AS phase, COUNT(*)
FROM strategy_signal_events
WHERE kind = 'entry'
  AND payload->>'direction' IN ('CLOSE_LONG', 'CLOSE_SHORT');

-- Verify exit row distribution post-fix.
SELECT 'after:exit_distribution' AS phase,
       payload->>'direction' AS direction, kind, side, status, COUNT(*)
FROM strategy_signal_events
WHERE kind = 'exit'
GROUP BY 2, 3, 4, 5
ORDER BY 2, 3, 4, 5;

-- ROLLBACK to dry-run, COMMIT to apply.
COMMIT;
