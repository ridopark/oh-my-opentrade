# S3. 13F + Options Skew Confirmation (Phase B outline)

## Thesis

13F filings reveal whale positions with 45-day lag. The naive trade (buy
what whales bought on filing day) is crowded. The edge: use changes in
options skew around the filing window to confirm whether the position is
still being built or has already been unwound.

**Declining put skew + whale disclosed long = accumulation likely ongoing.**

## Signal sketch

```
skew(symbol, expiry) = IV(25-delta put) - IV(25-delta call)
skew_z = (skew - mean_30d) / std_30d

ENTRY LONG when:
  - 13F shows new position or increased shares (from whale pipeline)
  - skew_z < -1.0 (put premium is declining → less downside hedging)
  - Filing was within last 10 trading days
  - Price has NOT already moved > 5% since filing date

EXIT:
  - skew_z reverts to > 0 (market re-pricing downside → accumulation may be over)
  - OR 20-day time exit
  - OR 8% trailing stop
```

## Data requirements

| Data | Available? | Notes |
|---|---|---|
| 13F filing data (new positions, increased shares) | ✅ | `cmd/omo-backfill-13f`, whale accumulation pipeline |
| Options chain IV per strike per expiry | ✅ | IBKR + DoltHub snapshots |
| 25-delta put/call strikes | Needs computation | Interpolate from the chain using Black-Scholes delta |
| 30-day trailing skew history | Needs storage | Add a daily `options_skew` table or compute on-the-fly |

## Implementation cost

- Skew calculator (25-delta interpolation): **4 hours**
- Signal logic + 13F filing date join: **4 hours**
- Strategy wiring: **2 hours**
- Backtest: **4 hours**
- **Total: ~2 days**

## Key difference from S1/S2

This is a **swing strategy** (1-20 day holds) rather than intraday. It
diversifies the portfolio across time horizons — S1/S2 are intraday, S3 is
multi-day, reducing correlation.

## References

- Cremers & Weinbaum (2010) "Deviations from Put-Call Parity and Stock
  Return Predictability" *JFE*
- Agarwal et al. (2013) "Uncovering Hedge Fund Skill from the Portfolio
  Holdings They Hide"
