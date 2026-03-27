# Historical Options Data Sources for Backtesting

## Research Report -- March 2026

Target data: Historical options chain snapshots (bid/ask/IV/Greeks/OI) for US equity
options on AMD, AAPL, TSLA, PLTR, HIMS, SOFI, SOXL, SPY, QQQ, NFLX.

---

## Table of Contents

1. Executive Summary and Ranking
2. Broker APIs You Already Have
3. Free Data Providers
4. Open Source / Community Sources
5. Building Your Own Database Going Forward
6. Hybrid / Synthetic Approaches
7. Very Cheap Providers ($25/month or less)
8. Recommended Implementation Plan

---

## 1. Executive Summary and Ranking

### Top-Tier (Recommended -- start here)

| Rank | Source | Cost | Historical Depth | Data Quality | Effort |
|------|--------|------|-----------------|--------------|--------|
| 1 | **Build your own via Alpaca + ivcollector (enhanced)** | Free (existing infra) | Forward-only, starts now | Excellent | Medium |
| 2 | **IBKR TWS API historical options data** | Free (with account) | 1-2 years for EOD | Good | Medium |
| 3 | **CBOE DataShop free files** | Free | 2-5 years (limited) | Excellent | Low |
| 4 | **Polygon.io Starter** | $29/mo | 2+ years full chains | Excellent | Low |
| 5 | **QuantConnect LEAN** | Free (in their cloud) | 15+ years | Excellent | Medium |

### Mid-Tier (Viable alternatives)

| Rank | Source | Cost | Historical Depth | Data Quality | Effort |
|------|--------|------|-----------------|--------------|--------|
| 6 | **Tradier API** | Free (with brokerage) | Current chains only | Good | Low |
| 7 | **OCC public reports** | Free | 2+ years | Partial (volume/OI only) | Medium |
| 8 | **Yahoo Finance options scraping** | Free | Current + ~2 weeks | Poor | High |
| 9 | **Kaggle datasets** | Free | Varies | Variable | Low |
| 10 | **VIX + synthetic IV approach** | Free | 20+ years | Approximation only | High |

### Lower-Tier (Limited utility)

| Rank | Source | Notes |
|------|--------|-------|
| 11 | Nasdaq Data Link (Quandl) | Options datasets mostly discontinued |
| 12 | Barchart | No free historical options API |
| 13 | Google Finance | No options data at all |
| 14 | SEC EDGAR | No options market data (filings only) |
| 15 | MarketWatch/WSJ | Display only, no API, no historical |

---

## 2. Broker APIs You Already Have

### 2A. Alpaca (your primary broker)

**What you already use**: Your `options_rest.go` calls two endpoints:
- `GET /v2/options/contracts` -- lists available contracts (broker API)
- `GET /v1beta1/options/snapshots` -- live snapshots with Greeks/IV/bid/ask (data API)
- `GET /v1beta1/options/bars` -- historical OHLCV bars for option contracts

**Historical options data availability**:

- **Snapshots**: Real-time only. There is NO historical snapshot endpoint. The
  `/v1beta1/options/snapshots` endpoint returns only the current state.
- **Historical bars**: The `/v1beta1/options/bars` endpoint provides OHLCV price bars
  for individual option contracts. This gives you historical option prices but NOT
  historical Greeks, IV, bid/ask spreads, or open interest.
- **Historical depth for bars**: Alpaca provides options bar data going back to
  approximately 2019-2020 for actively traded options.
- **Granularity**: 1Min, 5Min, 15Min, 1Hour, 1Day for option bars.
- **Rate limits**: 200 requests/minute on free plan. Options data requires at minimum
  a funded brokerage account.
- **Feed**: Your code uses `feed=indicative` which is the free OPRA-derived feed.
  The `sip` feed requires a paid market data subscription.

**What you CAN do with Alpaca for building history**:
- Your `ivcollector` service already snapshots ATM IV daily at 4:15 PM ET. This is
  exactly the right approach. It stores: time, symbol, atm_iv, atm_strike, spot_price,
  call_iv, put_iv in your `iv_snapshots` TimescaleDB hypertable.
- You could EXPAND this to capture the full chain (all strikes, all expiries) rather
  than just ATM. This is the single most impactful thing you can do.

**Alpaca limitations**:
- No historical options Greeks or IV snapshots
- No historical bid/ask quotes for options
- Options bar data does not include OI
- The indicative feed has 15-minute delay (but for EOD snapshots this is irrelevant)

**Documentation**: https://docs.alpaca.markets/docs/options-trading

### 2B. Interactive Brokers (IBKR) TWS API

**What you already use**: Your `ibkr/market_data.go` uses `ReqHistoricalData` and
`ReqRealTimeBars` for equity bars. You use the `scmhub/ibsync` Go library.

**Historical options data via IBKR**:

IBKR is significantly more capable than Alpaca for historical options data:

- **`reqHistoricalData` for options**: You can request historical bars for any
  individual option contract. Supports OHLCV, and also `OPTION_IMPLIED_VOLATILITY`
  and `HISTORICAL_VOLATILITY` as whatToShow parameters.

- **Key whatToShow values for options**:
  - `OPTION_IMPLIED_VOLATILITY` -- returns the underlying's IV derived from options
  - `HISTORICAL_VOLATILITY` -- returns realized volatility of the underlying
  - `TRADES` -- OHLCV trade data for the specific option contract
  - `BID_ASK` -- historical bid/ask data
  - `BID` / `ASK` -- individual bid or ask history

- **Historical depth**:
  - EOD bars: up to 1 year for option contracts
  - 1-minute bars: up to 5 days (TWS pacing limits)
  - IV and HV data for underlyings: several years

- **`reqSecDefOptParams`**: Returns available option expirations and strikes for an
  underlying. Useful for enumerating the chain.

- **`reqMktData` with snapshot=True**: Can get a one-time snapshot of any option
  including Greeks (tick types 10=bid, 11=ask, 13=model_iv, 80=delta, 81=gamma,
  etc.). But this is real-time only.

- **`calculateImpliedVolatility` and `calculateOptionPrice`**: IBKR's own BSM
  calculator -- you send it an option contract and price, it returns IV; or you send
  IV and it returns theoretical price.

- **Rate limits**: Strict pacing rules. Max 6 historical data requests per 2 seconds
  (for same instrument). Max 60 requests in 10 minutes. Concurrent requests limited.
  Your code already handles this with `maxConcurrentHistorical = 1` and 200ms spacing.

- **Cost**: Free with any funded IBKR account ($0 minimum). Market data subscriptions
  for real-time quotes are separate ($1.50/mo for US equity options via OPRA).

**What you CAN do with IBKR to build history**:
- Enumerate the options chain for each underlying using `reqSecDefOptParams`
- For each contract of interest, request historical bars with `OPTION_IMPLIED_VOLATILITY`
- Request historical `BID_ASK` data for EOD snapshots
- Build a daily cron job that captures a full chain snapshot

**IBKR limitations**:
- Pacing limits make bulk historical data collection slow (would take hours per day
  of history if you want full chains for 10 underlyings)
- No bulk download -- one contract at a time
- Historical Greeks are NOT directly available as a historical data type; you would
  need to collect IV and compute Greeks yourself via BSM
- Connection requires running TWS or IB Gateway

**Documentation**: https://ibkrcampus.com/ibkr-api-page/twsapi-doc/

### 2C. Recommendation for Broker APIs

**Immediate action**: Enhance your `ivcollector` to capture full option chain snapshots,
not just ATM IV. Store in a new `option_chain_snapshots` TimescaleDB table:

```
CREATE TABLE option_chain_snapshots (
    time           TIMESTAMPTZ NOT NULL,
    underlying     TEXT NOT NULL,
    contract_sym   TEXT NOT NULL,      -- OCC symbol
    expiry         DATE NOT NULL,
    strike         DOUBLE PRECISION NOT NULL,
    right          TEXT NOT NULL,      -- 'C' or 'P'
    bid            DOUBLE PRECISION,
    ask            DOUBLE PRECISION,
    last           DOUBLE PRECISION,
    iv             DOUBLE PRECISION,
    delta          DOUBLE PRECISION,
    gamma          DOUBLE PRECISION,
    theta          DOUBLE PRECISION,
    vega           DOUBLE PRECISION,
    open_interest  INTEGER,
    volume         INTEGER
);
SELECT create_hypertable('option_chain_snapshots', 'time');
```

This uses your existing Alpaca `GetOptionChain` function which already returns all
these fields. You would just need to iterate over more expiries (weekly + monthly
out to 60 DTE) and store both calls and puts.

---

## 3. Free Data Providers

### 3A. CBOE (Chicago Board Options Exchange)

**What is available for free**:

- **CBOE DataShop** (https://datashop.cboe.com/): CBOE publishes some free sample
  datasets and offers paid historical data. The free samples are typically 1-2 months
  of data for educational/evaluation purposes.

- **VIX Index historical data**: Free download from CBOE website going back to 1990.
  Daily OHLC for VIX. This is extremely useful as a market-wide IV proxy.
  URL: https://www.cboe.com/tradable_products/vix/vix_historical_data/

- **VIX Term Structure**: Free daily data for VIX futures term structure.

- **CBOE LiveVol** (https://www.cboe.com/data-services/livevol/): Commercial data
  service. Not free, but they occasionally offer academic/research access.

- **Free EOD options data**: CBOE does NOT provide free historical options chain
  data for individual equities. Their DataShop sells this data starting at
  ~$1,000/year for limited datasets.

**Verdict**: Only useful for VIX/volatility index data (which is genuinely excellent
and free). No free equity options chain data.

### 3B. OCC (Options Clearing Corporation)

**What is available for free**:

- **Daily volume and open interest reports**: OCC publishes daily reports at
  https://www.theocc.com/Market-Data/Market-Data-Reports
  - Volume by exchange
  - Open interest by series
  - Exercise and assignment activity

- **Historical volume data**: Monthly and annual volume statistics going back many
  years. Aggregated by product type, not by individual series.

- **Series-level OI data**: The "Series Search" tool provides current open interest
  for specific option series, but NOT historical OI.

- **Data format**: Reports are typically in CSV or PDF format.

**What is NOT available**:
- No historical bid/ask/last prices
- No historical IV or Greeks
- No historical intraday data

**Verdict**: Useful for validating open interest data but not sufficient for
backtesting. The volume reports can help identify liquid contracts.

### 3C. SEC EDGAR

**What is available**:
- 13F filings show institutional options holdings (quarterly, very delayed)
- Form 4 insider trading reports may mention option exercises
- No market data whatsoever

**Verdict**: Irrelevant for options backtesting.

### 3D. Yahoo Finance

**What is available**:

- **Current options chains**: Yahoo Finance provides current option chains via:
  - Web scraping: `https://finance.yahoo.com/quote/AAPL/options/`
  - Unofficial API: `https://query1.finance.yahoo.com/v7/finance/options/{symbol}`
  - Python library `yfinance`: `yf.Ticker("AAPL").options` and `.option_chain(date)`

- **Data fields**: Strike, bid, ask, last, volume, open interest, IV (calculated
  by Yahoo), contract name, expiry.

- **Historical options**: Yahoo does NOT provide historical options chain data.
  You can only see the current chain. Previous chain states are not archived.

- **Historical equity data**: Excellent, going back decades. Useful for underlying
  price history.

- **Rate limits**: Aggressive rate limiting. No official API. The unofficial endpoints
  can change or break without notice. IP bans are common with heavy scraping.

**What you CAN do**:
- Set up a daily scraper using `yfinance` to build your own options chain database
  going forward. This is a legitimate approach but:
  - Yahoo's IV calculation methodology is opaque
  - Greeks are not provided (you'd compute them yourself)
  - Data quality can be spotty (especially for illiquid contracts)
  - Terms of service technically prohibit automated scraping

**Verdict**: Useful as a supplementary free source for daily chain snapshots going
forward. Not a source of historical data. Your existing Alpaca integration is better
for this purpose since it provides Greeks directly.

### 3E. Google Finance

**What is available**: Nothing. Google Finance does not provide options data in any
form. It only shows basic equity quotes and charts.

**Verdict**: Completely irrelevant.

### 3F. Polygon.io

**What is available**:

- **Free tier ("Basic")**: $0/month
  - 5 API calls/minute
  - End-of-day equity data only
  - NO options data on free tier
  - 2-year history for equities

- **Starter tier**: $29/month
  - 5 API calls/minute (unlimited on some endpoints)
  - Full historical options data including:
    - Options chain snapshots (current)
    - Historical options aggregates (OHLCV bars)
    - Options contract details
  - Data goes back to 2019+
  - EOD and intraday granularity

- **Developer tier**: $79/month
  - Unlimited API calls
  - Same data as Starter but faster
  - WebSocket streaming

- **Options-specific endpoints**:
  - `GET /v3/reference/options/contracts` -- list all contracts
  - `GET /v3/snapshot/options/{underlyingAsset}` -- current chain snapshot
  - `GET /v2/aggs/ticker/{optionsTicker}/range/{multiplier}/{timespan}/{from}/{to}`
    -- historical bars for a specific option contract
  - `GET /v1/open-close/{optionsTicker}/{date}` -- daily open/close

- **Data fields**: OHLCV, but NOT historical IV/Greeks/bid-ask directly. You would
  need to compute IV from prices yourself.

**Verdict**: The Starter plan at $29/mo is one of the best value options if you
want actual historical option prices. However, it gives you OHLCV bars, not full
chain snapshots with Greeks. You would need to compute IV/Greeks from the price
data using BSM.

**Documentation**: https://polygon.io/docs/options

### 3G. Theta Data

**What is available**:

- **Free tier ("Value")**: As of 2025, Theta Data offers:
  - End-of-day options data
  - 15-minute delayed real-time
  - Rate limited
  - Basic historical options data

- **Standard tier**: $25/month
  - Full historical options data including:
    - EOD quotes (bid, ask, midpoint) for all US equity options
    - Historical open interest
    - Historical volume
    - Greeks and IV snapshots (computed by Theta Data)
  - Data goes back to 2013+ for most symbols
  - Bulk download available

- **Pro tier**: $50/month
  - Intraday quotes at various granularities
  - Tick-level data

- **Data quality**: Theta Data is specifically built for options data. Their Greeks
  calculations use a proper dividend-adjusted BSM model. Data is generally considered
  high quality in the retail quant community.

- **API style**: REST API with Python client library (`thetadata` package).

**Verdict**: Theta Data Standard at $25/month is arguably the single best value
proposition for exactly what you need. Full historical options chain data with
IV and Greeks going back to 2013. This should be your first choice if you are
willing to spend $25/month.

**Documentation**: https://www.thetadata.us/docs

### 3H. ORATS (Option Research and Technology Services)

**What is available**:

- **Free trial**: ORATS offers a 14-day free trial of their data.

- **Data API**: Starting at ~$99/month for the API
  - Historical IV surfaces
  - Historical Greeks for all strikes/expiries
  - Earnings and dividend estimates built into their models
  - Skew data
  - Data goes back to 2007+

- **ORATS Wheel** (free with limitations): Their web-based screener shows current
  options data but no historical access.

- **Academic discount**: ORATS offers academic pricing (significant discount).

**Verdict**: Too expensive for the budget constraint but excellent data quality.
The 14-day free trial is useful for getting a sample dataset for model validation.

### 3I. Nasdaq Data Link (formerly Quandl)

**What is available**:

- **Historical options data**: Quandl's options datasets (like the EOD US Stock
  Options dataset from "ORAT" provider) have been largely discontinued or moved
  behind paywalls since Nasdaq's acquisition.

- **Free datasets**: Some free equity data remains, but options-specific free
  datasets are essentially gone.

- **Wiki Continuous Futures**: Free, but irrelevant for equity options.

**Verdict**: No longer a viable source for free options data.

### 3J. Barchart

**What is available**:

- **Free web access**: Current options chains viewable on barchart.com
- **API**: Barchart's OnDemand API provides options data but requires a paid
  subscription (pricing not publicly listed, typically $50+/month).
- **Historical**: No free historical options data.

**Verdict**: Not useful for free historical data.

### 3K. MarketWatch / WSJ

**What is available**: Display-only current options chains on their websites.
No API. No historical data. No bulk access.

**Verdict**: Irrelevant.

---

## 4. Open Source / Community Sources

### 4A. QuantConnect LEAN

**What is available**:

- **Historical options data**: QuantConnect provides comprehensive historical US
  equity options data for FREE within their cloud research environment.
  - Full chain data: all strikes, all expiries
  - Minute-resolution quotes
  - Greeks (computed by QuantConnect)
  - Open interest
  - Data goes back to January 2010 for most symbols
  - Coverage: All US equity options

- **How to access**:
  - Create a free QuantConnect account
  - Use their cloud-based Jupyter notebooks or Algorithm Lab
  - Write strategies in Python or C#
  - Data cannot be bulk-downloaded or exported (this is the catch)

- **LEAN Engine (open source)**: The backtesting engine is open source on GitHub
  (https://github.com/QuantConnect/Lean). However, the data is NOT included.
  You need to either:
  - Use QuantConnect's cloud (free, but data stays in their environment)
  - Purchase data separately from QuantConnect's Data Library

- **QuantConnect Data Library**: You can purchase individual datasets. Options data
  is priced per symbol per year. Typically $5-15 per symbol-year for minute data.
  For 10 symbols over 5 years, this could be $250-750 one-time.

- **Research notebooks**: You CAN export aggregated results (e.g., backtest
  performance metrics, processed CSV summaries) from their cloud. You just cannot
  bulk-export the raw tick data.

**Verdict**: This is the best free source of historical options data IF you are
willing to run your backtest within QuantConnect's cloud environment. For your
use case (backtesting a specific strategy), you could prototype in QuantConnect,
validate results, then use the insights to calibrate a synthetic model in your
own system. Alternatively, buy data for your 10 symbols -- the one-time cost
is reasonable.

**Documentation**: https://www.quantconnect.com/docs/v2/research-environment

### 4B. Kaggle Datasets

**What is available**:

- **"Huge Stock Market Dataset" by Boris Marjanovic**: Equity prices only, no options.

- **"Options Data" datasets**: Several user-uploaded datasets exist:
  - "SPY Options Data" -- Some users have uploaded SPY options chain snapshots
    covering a few months to a year. Quality varies wildly.
  - "Options Market Data" -- Occasional uploads of options data scraped from
    various sources. Typically covers a narrow time window.

- **Search**: https://www.kaggle.com/search?q=options+chain+data

- **Typical issues**:
  - Small time windows (weeks to months, rarely years)
  - Incomplete chains (only ATM or popular strikes)
  - No standardized format
  - May not include Greeks or IV
  - Provenance unclear (potentially scraped in violation of ToS)

**Verdict**: Worth checking periodically for SPY/QQQ datasets that could serve
as validation data. Do not rely on this as a primary source.

### 4C. GitHub Repositories

**Notable repos**:

- **`ranaroussi/yfinance`** (Python): Library for Yahoo Finance data. Can fetch
  current options chains. No historical options capability.
  https://github.com/ranaroussi/yfinance

- **`addisonlynch/iexfinance`**: IEX Cloud API wrapper. IEX does not provide
  options data.

- **`sdrdis/hotmetal`**: Historical options data collector -- but appears
  unmaintained and uses defunct data sources.

- **`vollib`**: Options pricing library (BSM, binomial). Useful for computing
  Greeks from price data. https://github.com/vollib/vollib

- **`quantlib`**: The QuantLib project via `QuantLib-Python`. Professional-grade
  options pricing and analytics. Essential if you are computing Greeks from
  historical price data yourself.

**Verdict**: No significant open-source historical options datasets exist on
GitHub. The pricing libraries (vollib, QuantLib) are useful tools for computing
Greeks from historical price data you obtain elsewhere.

### 4D. Reddit Communities

**Relevant discussions**:

- **r/algotrading**: Frequent threads about free options data. The consensus is
  that truly free historical options data with Greeks essentially does not exist.
  Common recommendations point to Theta Data, Polygon, or building your own.

- **r/options**: More trading-focused, less data-focused. Occasional data sharing
  posts but nothing systematic.

- **r/quantfinance**: Academic discussions. Points to WRDS/OptionMetrics for
  serious research (see Academic sources below).

### 4E. Academic Sources

- **WRDS (Wharton Research Data Services)**: Hosts OptionMetrics IvyDB, the gold
  standard for academic options research. Daily and intraday options data going back
  to 1996. Free for affiliated university researchers. Not available to the public.

- **OptionMetrics**: The underlying data provider for WRDS options data. Commercial
  access starts at ~$10,000/year.

- **If you have university affiliation**: Check if your institution has WRDS access.
  This would give you the highest quality historical options data available anywhere.

---

## 5. Building Your Own Database Going Forward

### 5A. Enhanced ivcollector Approach (RECOMMENDED)

Your existing `ivcollector` service runs daily at 4:15 PM ET and captures ATM IV
for your symbol list. Here is how to expand it:

**Current state**: Stores 7 fields per symbol per day in `iv_snapshots`.

**Enhanced state**: Store the full option chain snapshot. For each symbol, capture
all contracts within a reasonable window (e.g., expiries out to 60 DTE, strikes
within +/-20% of spot).

**Estimated data volume per day** (for 10 symbols):
- Average liquid underlying: ~4 weekly/monthly expiries within 60 DTE
- Average strikes per expiry: ~30 (15 calls + 15 puts)
- Contracts per symbol: ~120
- Total contracts per day: 10 symbols x 120 = 1,200 rows
- Row size: ~200 bytes
- Daily storage: ~240 KB
- Annual storage: ~60 MB
- With TimescaleDB compression: ~15 MB/year

This is trivially small. Storage is not a constraint.

**Alpaca API cost for this**:
- 1,200 contracts / 100 per batch = 12 snapshot API calls
- Plus 10 contract listing calls (one per underlying)
- Total: ~22 API calls per day
- Well within the 200 calls/minute free tier limit

**Time to useful backtesting data**:
- 30 days: Enough to validate your data pipeline
- 90 days: Enough for basic strategy parameter estimation
- 6 months: Enough for seasonal pattern analysis
- 1 year: Minimum for credible single-strategy backtesting
- 2+ years: Adequate for multi-regime backtesting (if it spans a vol event)

### 5B. IBKR-Based Collection

You could also use IBKR to collect historical option data retroactively:

- Use `reqSecDefOptParams` to get the chain structure
- Use `reqHistoricalData` with `whatToShow=BID_ASK` for EOD bid/ask
- Use `reqHistoricalData` with `whatToShow=OPTION_IMPLIED_VOLATILITY` for IV
- Backfill ~1 year of EOD data per symbol

**Pacing estimate**:
- 10 symbols x ~120 contracts x 2 requests (price + IV) = 2,400 requests
- At IBKR's rate limit (~6/2sec for same instrument, ~60/10min): ~40 minutes
  per day of backfill
- 252 trading days x 40 min = ~168 hours = 7 days of continuous collection
  (but you would spread this over weeks)

**Limitation**: IBKR only provides ~1 year of historical EOD data for option
contracts. Expired contracts are progressively harder to query.

### 5C. Dual-Source Collection

Run both Alpaca (primary) and IBKR (validation) collectors simultaneously.
Cross-validate the data. Use IBKR to backfill the year before you started
collecting with Alpaca.

---

## 6. Hybrid / Synthetic Approaches

### 6A. VIX + ATR Synthetic IV Model

You already have ATR calculations in your backtest engine and VIX data is free.
Here is how to create a synthetic IV estimate:

**Free data you can combine**:
1. VIX Index (daily, from CBOE, back to 1990)
2. Realized volatility of the underlying (compute from free Yahoo/Alpaca OHLCV data)
3. VIX term structure (free from CBOE)
4. Your own ATM IV snapshots (from ivcollector, going forward)
5. Earnings dates (free from Yahoo Finance / SEC EDGAR)

**Synthetic IV model**:
```
IV_underlying(t) = alpha * VIX(t) + beta * RealizedVol_20d(t) + gamma * BetaToSPY * VIX(t)
```

Where:
- alpha, beta, gamma are calibrated from your collected ATM IV snapshots
- BetaToSPY is the stock's beta (computable from free price data)
- RealizedVol_20d is the 20-day realized volatility

**Calibration approach**:
1. Once you have 90+ days of actual ATM IV from ivcollector, regress against VIX
   and RealizedVol to estimate alpha, beta, gamma per symbol.
2. Apply this model retroactively to generate synthetic historical IV.
3. Validate by comparing the synthetic IV to your actual collected IV during the
   overlap period.

**Quality estimate**: This approach typically achieves R-squared of 0.7-0.85 for
ATM IV prediction. It captures the broad level of IV well but misses:
- Stock-specific IV events (earnings, FDA approvals, etc.)
- Skew/smile dynamics
- Term structure nuances
- Sudden idiosyncratic IV spikes

**For your ORB strategy**: If your strategy primarily needs to know whether IV is
"high" or "low" relative to history (which your IVRank/IVPercentile system already
does), this synthetic model may be sufficient for position sizing and stop-loss
calibration. It is NOT sufficient for options pricing backtests.

### 6B. Free EOD Options Data Calibration

If you obtain even partial historical options data (e.g., from Kaggle, a Theta
Data free trial, or QuantConnect exports), you can use it to:

1. Calibrate the synthetic model above for each symbol
2. Establish typical IV-to-RealizedVol ratios per symbol
3. Characterize typical skew shapes for each underlying
4. Estimate typical bid-ask spreads as a function of moneyness and DTE

### 6C. ATM IV History for Free

Several sources provide just ATM IV (without full chains):

1. **Your own ivcollector**: Already collecting this. Best source going forward.
2. **CBOE VIX**: SPY ATM IV proxy (free, 30+ years).
3. **iVolatility.com**: Provides free 1-year IV charts for individual stocks. No
   API, but the charts show 30-day ATM IV history. You could manually record
   reference points for calibration.
4. **MarketChameleon**: Shows free IV charts with limited history. No API.
5. **Barchart**: Shows current IV rank/percentile on free tier.

---

## 7. Very Cheap Providers ($25/month or less)

### 7A. Theta Data Standard -- $25/month (TOP PICK)

- **Data**: Full historical US equity options EOD data
  - Bid, ask, midpoint, last price
  - Volume, open interest
  - Greeks (delta, gamma, theta, vega, rho)
  - Implied volatility
  - All strikes, all expiries
- **History**: Back to 2013 for most symbols
- **Granularity**: End of day
- **Coverage**: All US equity options (OPRA universe)
- **Format**: REST API, Python client, bulk CSV download
- **Rate limits**: Reasonable for EOD data (specific limits vary by plan)
- **URL**: https://www.thetadata.us

This is by far the best value for your specific needs.

### 7B. Polygon.io Starter -- $29/month

- **Data**: Historical options OHLCV bars, contract details
  - Prices (open, high, low, close)
  - Volume
  - NO pre-computed Greeks or IV (you must compute these yourself)
- **History**: Back to ~2019
- **Granularity**: 1-minute to daily
- **Coverage**: All US equity options
- **Format**: REST API, WebSocket
- **Rate limits**: 5 API calls/minute on Starter (can be limiting for bulk download)
- **URL**: https://polygon.io

Good value but requires you to compute IV and Greeks yourself, which adds
implementation effort.

### 7C. Tradier -- Free (with brokerage account)

- **Data**: Current options chain with Greeks
  - Full chain snapshots (current only)
  - Bid, ask, last, volume, OI
  - Greeks and IV (computed by Tradier)
  - Historical options OHLCV via their API
- **History**: Limited historical options data (~1-2 years for active contracts)
- **Granularity**: EOD for historical, real-time for current
- **Coverage**: All US equity options
- **Requirement**: Must open a Tradier brokerage account (no minimum balance)
- **URL**: https://documentation.tradier.com/brokerage-api/markets/get-options-chains

Worth opening an account for the free real-time options chain data. Good
supplementary source.

### 7D. Unusual Whales -- $17-25/month

- **Data**: Options flow data, unusual activity
  - NOT full historical chains
  - Historical options flow/unusual activity
  - IV percentile/rank data
- **History**: 1-2 years
- **Coverage**: US equity options
- **URL**: https://unusualwhales.com

Not directly useful for backtesting options pricing but could supplement
flow-based strategy research.

### 7E. Market Data App -- $9/month

- **Data**: Options chain snapshots
  - Current chains with Greeks
  - Limited historical
- **History**: Limited
- **Coverage**: US equity options
- **URL**: https://www.marketdata.app
- **Note**: Relatively new service, still maturing

---

## 8. Recommended Implementation Plan

### Phase 1: Immediate (Week 1) -- Free, using existing infrastructure

1. **Enhance ivcollector** to capture full option chain snapshots daily:
   - Expand from ATM-only to all strikes within +/-20% of spot
   - Capture 3-5 expiry dates per symbol (weeklies + monthlies out to 60 DTE)
   - Store bid/ask/last/IV/delta/gamma/theta/vega/OI
   - Create the `option_chain_snapshots` TimescaleDB table
   - This uses your existing Alpaca `GetOptionChain` function with no new
     dependencies

2. **Download free VIX data** from CBOE:
   - VIX daily OHLC back to 1990
   - VIX9D (9-day), VIX3M (3-month), VIX6M (6-month) for term structure
   - Store in TimescaleDB

3. **Compute realized volatility** for all 10 symbols from historical equity
   prices (you already have this data from Alpaca).

### Phase 2: Short-term (Weeks 2-4) -- Free, using IBKR

4. **Backfill ~1 year of historical option IV data via IBKR**:
   - Use `reqHistoricalData` with `OPTION_IMPLIED_VOLATILITY` for each underlying
   - Use `reqHistoricalData` with `BID_ASK` for ATM contracts
   - Run during off-hours to avoid pacing issues with live trading
   - This gives you a jump start on historical data

5. **Calibrate synthetic IV model**:
   - Regress collected ATM IV against VIX, realized vol, and beta
   - Generate synthetic IV history back 5+ years
   - Validate against IBKR backfilled data

### Phase 3: Optional investment (Month 2) -- $25/month

6. **Subscribe to Theta Data Standard** if you need full chain backtesting:
   - Download 5+ years of historical EOD options data for your 10 symbols
   - Import into your TimescaleDB
   - Use for full-fidelity options strategy backtesting
   - Cancel after initial bulk download if you only need the data, not ongoing
     access (data is yours to keep once downloaded)

### Phase 4: Ongoing -- Free

7. **Continue daily collection** via enhanced ivcollector
8. **Cross-validate** Alpaca vs IBKR vs Theta Data (if subscribed) to ensure
   data quality
9. After 6-12 months, you will have a robust proprietary options data set that
   requires no external paid subscription

### Cost Summary

| Phase | Cost | What You Get |
|-------|------|-------------|
| Phase 1 | $0 | Daily chain snapshots going forward + VIX + RV history |
| Phase 2 | $0 | ~1 year IBKR backfill + synthetic IV back 5+ years |
| Phase 3 | $25 one month | 5+ years full chain data from Theta Data |
| Phase 4 | $0 | Ongoing proprietary database |

**Total cost for comprehensive historical options data: $25 (one month of Theta Data)
plus development time.**

---

## Appendix: Data Field Requirements Mapping

| Field | Alpaca (live) | IBKR (hist) | Theta Data | Polygon | Your BSM |
|-------|:---:|:---:|:---:|:---:|:---:|
| Bid | Yes | Yes | Yes | No | No |
| Ask | Yes | Yes | Yes | No | No |
| Last | Yes | Yes | Yes | Yes | No |
| IV | Yes | Yes | Yes | No | Compute |
| Delta | Yes | No | Yes | No | Compute |
| Gamma | Yes | No | Yes | No | Compute |
| Theta | Yes | No | Yes | No | Compute |
| Vega | Yes | No | Yes | No | Compute |
| OI | Yes | No | Yes | No | No |
| Volume | No | Yes | Yes | Yes | No |

Your system already has a BSM implementation in `backend/internal/app/options/bsm.go`
that can compute Greeks from price + IV, so the "Compute" entries above are
feasible with your existing code.
