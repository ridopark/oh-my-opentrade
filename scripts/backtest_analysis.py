import numpy as np
import pandas as pd
from collections import defaultdict

# --- Raw trade data ---
trades = [
    (1, "MSFT", "LONG", "Mar 2", "10:09", -6.93, -0.10),
    (2, "HIMS", "LONG", "Mar 2", "10:34", 99.65, 1.42),
    (3, "HIMS", "LONG", "Mar 2", "11:39", 27.05, 0.39),
    (4, "PLTR", "LONG", "Mar 2", "11:34", 47.73, 0.69),
    (5, "META", "LONG", "Mar 2", "11:34", 41.96, 0.64),
    (6, "MSFT", "LONG", "Mar 2", "11:39", -7.97, -0.12),
    (7, "META", "SHORT", "Mar 3", "10:49", -22.70, -0.35),
    (8, "BAC", "LONG", "Mar 3", "10:59", 37.03, 0.53),
    (9, "AMZN", "LONG", "Mar 3", "11:24", 21.89, 0.31),
    (10, "PLTR", "LONG", "Mar 4", "10:44", -35.73, -0.51),
    (11, "SOXL", "LONG", "Mar 4", "10:54", 15.00, 0.22),
    (12, "PLTR", "LONG", "Mar 4", "11:04", -45.84, -0.66),
    (13, "SOXL", "LONG", "Mar 4", "11:29", 69.26, 0.99),
    (14, "PLTR", "LONG", "Mar 5", "10:09", -45.67, -0.65),
    (15, "META", "LONG", "Mar 5", "09:59", -27.73, -0.41),
    (16, "NFLX", "SHORT", "Mar 5", "10:44", -46.75, -0.67),
    (17, "BAC", "SHORT", "Mar 5", "10:19", 9.85, 0.14),
    (18, "PLTR", "LONG", "Mar 6", "10:14", -41.60, -0.60),
    (19, "SOFI", "LONG", "Mar 6", "10:14", -7.00, -0.10),
    (20, "SOFI", "LONG", "Mar 6", "10:59", -27.55, -0.39),
    (21, "NFLX", "SHORT", "Mar 6", "11:09", -5.20, -0.07),
    (22, "SOFI", "SHORT", "Mar 10", "10:09", -43.58, -0.62),
    (23, "HIMS", "SHORT", "Mar 10", "10:34", 263.25, 3.77),
    (24, "SOXL", "LONG", "Mar 10", "10:34", 148.77, 2.13),
    (25, "META", "LONG", "Mar 10", "11:14", -17.27, -0.26),
    (26, "AMZN", "LONG", "Mar 10", "11:44", -8.78, -0.13),
    (27, "HIMS", "LONG", "Mar 11", "10:14", 160.98, 2.30),
    (28, "SOXL", "LONG", "Mar 11", "10:14", -54.13, -0.78),
    (29, "AMZN", "SHORT", "Mar 11", "10:09", 19.05, 0.28),
    (30, "SOFI", "SHORT", "Mar 11", "11:09", -1.20, -0.02),
    (31, "PLTR", "SHORT", "Mar 11", "11:29", 2.74, 0.04),
    (32, "MSFT", "SHORT", "Mar 12", "10:34", -35.43, -0.52),
    (33, "SOXL", "SHORT", "Mar 13", "11:19", 1.19, 0.02),
    (34, "SOXL", "LONG", "Mar 23", "10:14", 74.86, 1.07),
    (35, "AMZN", "LONG", "Mar 23", "10:14", -33.82, -0.50),
    (36, "AMZN", "LONG", "Mar 23", "10:49", -16.70, -0.25),
    (37, "META", "LONG", "Mar 23", "11:44", -37.47, -0.56),
    (38, "SOXL", "LONG", "Mar 24", "10:34", -57.39, -0.82),
    (39, "MSFT", "SHORT", "Mar 24", "10:59", -18.33, -0.27),
]

df = pd.DataFrame(trades, columns=["id", "symbol", "side", "date", "time", "pnl", "pct"])

initial_equity = 100_000

# ============================================================
# 1. DETAILED PERFORMANCE METRICS
# ============================================================
print("=" * 70)
print("1. DETAILED PERFORMANCE METRICS")
print("=" * 70)

total_trades = len(df)
wins = df[df["pnl"] > 0]
losses = df[df["pnl"] <= 0]
n_wins = len(wins)
n_losses = len(losses)
win_rate = n_wins / total_trades

total_pnl = df["pnl"].sum()
avg_pnl = df["pnl"].mean()
median_pnl = df["pnl"].median()

avg_win = wins["pnl"].mean()
avg_loss = losses["pnl"].mean()
max_win = wins["pnl"].max()
max_loss = losses["pnl"].min()

# Profit factor
gross_profit = wins["pnl"].sum()
gross_loss = abs(losses["pnl"].sum())
profit_factor = gross_profit / gross_loss if gross_loss != 0 else float("inf")

# Expectancy per trade
expectancy = avg_pnl

# Win/loss ratio
win_loss_ratio = abs(avg_win / avg_loss)

# Max drawdown from equity curve
equity_curve = initial_equity + df["pnl"].cumsum()
running_max = equity_curve.cummax()
drawdown = equity_curve - running_max
max_drawdown_dollar = drawdown.min()
max_drawdown_pct = (drawdown / running_max).min() * 100

# Find drawdown duration
dd_end_idx = drawdown.idxmin()
# Find the peak before the max drawdown
peak_before = equity_curve[:dd_end_idx + 1].idxmax()

# Sharpe-like ratio (per trade, then annualized)
# Using daily returns grouped by date
daily_pnl = df.groupby("date")["pnl"].sum()
daily_returns = daily_pnl / initial_equity  # simple approximation

trading_days_in_sample = len(daily_pnl)
trading_days_per_year = 252

if daily_returns.std() > 0:
    daily_sharpe = daily_returns.mean() / daily_returns.std()
    annualized_sharpe = daily_sharpe * np.sqrt(trading_days_per_year)
else:
    annualized_sharpe = 0

# Sortino (downside deviation only)
downside_returns = daily_returns[daily_returns < 0]
downside_std = np.sqrt((downside_returns ** 2).mean())
if downside_std > 0:
    daily_sortino = daily_returns.mean() / downside_std
    annualized_sortino = daily_sortino * np.sqrt(trading_days_per_year)
else:
    annualized_sortino = 0

# Per-trade Sharpe (alternative calculation)
trade_returns = df["pct"] / 100
per_trade_sharpe = trade_returns.mean() / trade_returns.std() if trade_returns.std() > 0 else 0
# Annualize: avg ~39 trades over ~18 trading days => ~2.17 trades/day
trades_per_day = total_trades / trading_days_in_sample
annualized_per_trade_sharpe = per_trade_sharpe * np.sqrt(trades_per_day * trading_days_per_year)

print(f"Total Trades:          {total_trades}")
print(f"Winners / Losers:      {n_wins}W / {n_losses}L")
print(f"Win Rate:              {win_rate:.1%}")
print(f"")
print(f"Total P&L:             ${total_pnl:+,.2f}")
print(f"Avg P&L per trade:     ${avg_pnl:+,.2f}")
print(f"Median P&L per trade:  ${median_pnl:+,.2f}")
print(f"")
print(f"Avg Win:               ${avg_win:+,.2f}  ({wins['pct'].mean():+.2f}%)")
print(f"Avg Loss:              ${avg_loss:+,.2f}  ({losses['pct'].mean():+.2f}%)")
print(f"Largest Win:           ${max_win:+,.2f}  (Trade #{wins['pnl'].idxmax() + 1})")
print(f"Largest Loss:          ${max_loss:+,.2f}  (Trade #{losses['pnl'].idxmin() + 1})")
print(f"")
print(f"Win/Loss Ratio:        {win_loss_ratio:.2f}")
print(f"Profit Factor:         {profit_factor:.3f}")
print(f"Expectancy per trade:  ${expectancy:+,.2f}")
print(f"")
print(f"Gross Profit:          ${gross_profit:+,.2f}")
print(f"Gross Loss:            ${-gross_loss:+,.2f}")
print(f"")
print(f"Max Drawdown:          ${max_drawdown_dollar:,.2f}  ({max_drawdown_pct:.3f}%)")
print(f"  Peak trade index:    #{peak_before + 1}")
print(f"  Trough trade index:  #{dd_end_idx + 1}")
print(f"")
print(f"Trading days in sample:     {trading_days_in_sample}")
print(f"Avg trades per day:         {trades_per_day:.1f}")
print(f"")
print(f"Daily Sharpe (annualized):  {annualized_sharpe:.3f}")
print(f"Daily Sortino (annualized): {annualized_sortino:.3f}")
print(f"Per-trade Sharpe (ann.):    {annualized_per_trade_sharpe:.3f}")

# Consecutive losses
streaks = []
current_streak = 0
max_loss_streak = 0
for _, row in df.iterrows():
    if row["pnl"] <= 0:
        current_streak += 1
        max_loss_streak = max(max_loss_streak, current_streak)
    else:
        if current_streak > 0:
            streaks.append(current_streak)
        current_streak = 0
if current_streak > 0:
    streaks.append(current_streak)

print(f"Max consecutive losses:     {max_loss_streak}")

# ============================================================
# 2. ANALYSIS BY SYMBOL
# ============================================================
print(f"\n{'=' * 70}")
print("2. ANALYSIS BY SYMBOL")
print("=" * 70)

symbol_stats = []
for sym in sorted(df["symbol"].unique()):
    sub = df[df["symbol"] == sym]
    sw = sub[sub["pnl"] > 0]
    sl = sub[sub["pnl"] <= 0]
    s_total = sub["pnl"].sum()
    s_wr = len(sw) / len(sub) if len(sub) > 0 else 0
    s_avg = sub["pnl"].mean()
    s_gp = sw["pnl"].sum() if len(sw) > 0 else 0
    s_gl = abs(sl["pnl"].sum()) if len(sl) > 0 else 0
    s_pf = s_gp / s_gl if s_gl > 0 else float("inf")
    symbol_stats.append({
        "symbol": sym, "trades": len(sub), "wins": len(sw), "losses": len(sl),
        "win_rate": s_wr, "total_pnl": s_total, "avg_pnl": s_avg,
        "profit_factor": s_pf, "best": sub["pnl"].max(), "worst": sub["pnl"].min()
    })

sym_df = pd.DataFrame(symbol_stats).sort_values("total_pnl", ascending=False)
print(f"\n{'Symbol':<8} {'Trades':>6} {'W/L':>6} {'WR%':>6} {'Total P&L':>12} {'Avg P&L':>10} {'PF':>6} {'Best':>10} {'Worst':>10}")
print("-" * 85)
for _, r in sym_df.iterrows():
    pf_str = f"{r['profit_factor']:.2f}" if r['profit_factor'] < 100 else "INF"
    print(f"{r['symbol']:<8} {r['trades']:>6} {r['wins']}/{r['losses']:<4} {r['win_rate']:>5.0%} ${r['total_pnl']:>+10,.2f} ${r['avg_pnl']:>+8,.2f} {pf_str:>6} ${r['best']:>+8,.2f} ${r['worst']:>+8,.2f}")

profitable = sym_df[sym_df["total_pnl"] > 0]
losing = sym_df[sym_df["total_pnl"] <= 0]
print(f"\nProfitable symbols: {', '.join(profitable['symbol'].tolist())} (total: ${profitable['total_pnl'].sum():+,.2f})")
print(f"Losing symbols:     {', '.join(losing['symbol'].tolist())} (total: ${losing['total_pnl'].sum():+,.2f})")

# ============================================================
# 3. ANALYSIS BY SIDE (LONG vs SHORT)
# ============================================================
print(f"\n{'=' * 70}")
print("3. ANALYSIS BY SIDE (LONG vs SHORT)")
print("=" * 70)

for side in ["LONG", "SHORT"]:
    sub = df[df["side"] == side]
    sw = sub[sub["pnl"] > 0]
    sl = sub[sub["pnl"] <= 0]
    s_gp = sw["pnl"].sum() if len(sw) > 0 else 0
    s_gl = abs(sl["pnl"].sum()) if len(sl) > 0 else 0
    s_pf = s_gp / s_gl if s_gl > 0 else float("inf")
    print(f"\n--- {side} ---")
    print(f"  Trades:        {len(sub)}")
    print(f"  Win/Loss:      {len(sw)}W / {len(sl)}L  ({len(sw)/len(sub):.0%} win rate)")
    print(f"  Total P&L:     ${sub['pnl'].sum():+,.2f}")
    print(f"  Avg P&L:       ${sub['pnl'].mean():+,.2f}")
    print(f"  Profit Factor: {s_pf:.3f}")
    print(f"  Avg Win:       ${sw['pnl'].mean():+,.2f}" if len(sw) > 0 else "  Avg Win:       N/A")
    print(f"  Avg Loss:      ${sl['pnl'].mean():+,.2f}" if len(sl) > 0 else "  Avg Loss:      N/A")
    print(f"  Best trade:    ${sub['pnl'].max():+,.2f}")
    print(f"  Worst trade:   ${sub['pnl'].min():+,.2f}")

# ============================================================
# 4. RISK ANALYSIS
# ============================================================
print(f"\n{'=' * 70}")
print("4. RISK ANALYSIS")
print("=" * 70)

# Position sizing
print(f"\nPosition Sizing:")
print(f"  Avg absolute P&L:   ${df['pnl'].abs().mean():.2f}")
print(f"  Avg % move:         {df['pct'].abs().mean():.2f}%")
print(f"  Max single loss:    ${df['pnl'].min():+,.2f} ({df.loc[df['pnl'].idxmin(), 'pct']:.2f}%)")
print(f"  Max single loss as % of equity: {abs(df['pnl'].min()) / initial_equity * 100:.4f}%")
print(f"  Implied avg position size: ~${df['pnl'].abs().mean() / (df['pct'].abs().mean()/100):.0f}")

# Consecutive loss analysis
print(f"\nConsecutive Loss Streaks:")
losses_list = []
current = []
for _, row in df.iterrows():
    if row["pnl"] <= 0:
        current.append(row)
    else:
        if current:
            losses_list.append(current)
            current = []
if current:
    losses_list.append(current)

for streak in sorted(losses_list, key=len, reverse=True)[:3]:
    streak_pnl = sum(r["pnl"] for r in streak)
    ids = [str(r["id"]) for r in streak]
    print(f"  {len(streak)}-trade streak (#{', #'.join(ids)}): ${streak_pnl:+,.2f}")

# Daily P&L distribution
print(f"\nDaily P&L Distribution:")
print(f"  Best day:   ${daily_pnl.max():+,.2f}  ({daily_pnl.idxmax()})")
print(f"  Worst day:  ${daily_pnl.min():+,.2f}  ({daily_pnl.idxmin()})")
print(f"  Avg day:    ${daily_pnl.mean():+,.2f}")
print(f"  Std dev:    ${daily_pnl.std():.2f}")
print(f"  Winning days: {(daily_pnl > 0).sum()} / {len(daily_pnl)}")

# Equity curve stats
print(f"\nEquity Curve:")
print(f"  Starting:   ${initial_equity:,.2f}")
print(f"  Ending:     ${initial_equity + total_pnl:,.2f}")
print(f"  Peak:       ${(initial_equity + df['pnl'].cumsum()).max():,.2f}")
print(f"  Trough:     ${(initial_equity + df['pnl'].cumsum()).min():,.2f}")

# Intraday risk: max exposure per day
print(f"\nPer-Day Trade Count:")
day_counts = df.groupby("date").size()
print(f"  Max trades in a day:  {day_counts.max()} ({day_counts.idxmax()})")
print(f"  Min trades in a day:  {day_counts.min()}")
print(f"  Avg trades per day:   {day_counts.mean():.1f}")

# ============================================================
# 5. STATISTICAL SIGNIFICANCE / OUTLIER ANALYSIS
# ============================================================
print(f"\n{'=' * 70}")
print("5. STATISTICAL SIGNIFICANCE & OUTLIER ANALYSIS")
print("=" * 70)

# T-test: is mean P&L significantly different from zero?
from scipy import stats

t_stat, p_value = stats.ttest_1samp(df["pnl"], 0)
print(f"\nOne-sample t-test (H0: mean P&L = 0):")
print(f"  Mean P&L:    ${avg_pnl:+,.2f}")
print(f"  Std Dev:     ${df['pnl'].std():.2f}")
print(f"  t-statistic: {t_stat:.4f}")
print(f"  p-value:     {p_value:.4f}")
print(f"  Significant at 5%? {'YES' if p_value < 0.05 else 'NO'}")
print(f"  Significant at 10%? {'YES' if p_value < 0.10 else 'NO'}")

# Outlier impact
print(f"\nOutlier Analysis:")
# Top 3 trades by absolute P&L
top3 = df.nlargest(3, "pnl")
print(f"  Top 3 winners: ${top3['pnl'].sum():+,.2f}")
for _, r in top3.iterrows():
    print(f"    #{r['id']} {r['symbol']} {r['side']} {r['date']}: ${r['pnl']:+,.2f}")

without_top3 = df.drop(top3.index)["pnl"].sum()
print(f"\n  Total P&L without top 3:  ${without_top3:+,.2f}")
print(f"  Top 3 contribution:       ${top3['pnl'].sum():+,.2f} ({top3['pnl'].sum()/total_pnl*100:.1f}% of total)")

# Without #23 (HIMS SHORT +$263.25)
without_23 = total_pnl - 263.25
print(f"\n  Without trade #23 alone:  ${without_23:+,.2f}")
print(f"  Trade #23 is {263.25/total_pnl*100:.1f}% of total P&L")

# Concentration: top N trades as % of gross profit
print(f"\n  Gross Profit Concentration:")
sorted_wins = wins.sort_values("pnl", ascending=False)
cumulative = 0
for i, (_, r) in enumerate(sorted_wins.iterrows()):
    cumulative += r["pnl"]
    pct_of_gp = cumulative / gross_profit * 100
    print(f"    Top {i+1} winner(s): ${cumulative:+,.2f} = {pct_of_gp:.1f}% of gross profit")
    if pct_of_gp > 90:
        break

# Bootstrap confidence interval for mean P&L
np.random.seed(42)
n_bootstrap = 10000
boot_means = []
for _ in range(n_bootstrap):
    sample = df["pnl"].sample(n=total_trades, replace=True)
    boot_means.append(sample.mean())
boot_means = np.array(boot_means)
ci_lower = np.percentile(boot_means, 2.5)
ci_upper = np.percentile(boot_means, 97.5)
pct_positive = (boot_means > 0).mean()

print(f"\n  Bootstrap (10,000 resamples):")
print(f"    95% CI for mean P&L: [${ci_lower:+,.2f}, ${ci_upper:+,.2f}]")
print(f"    Prob(mean > 0):      {pct_positive:.1%}")

# ============================================================
# 6. RECOMMENDATIONS
# ============================================================
print(f"\n{'=' * 70}")
print("6. RECOMMENDATIONS")
print("=" * 70)

# Time-of-entry analysis
print(f"\nEntry Time Analysis:")
def time_bucket(t):
    h, m = map(int, t.split(":"))
    total = h * 60 + m
    if total < 630:  # before 10:30
        return "09:30-10:29"
    elif total < 690:  # before 11:30
        return "10:30-11:29"
    else:
        return "11:30-12:00"

df["time_bucket"] = df["time"].apply(time_bucket)
for bucket in ["09:30-10:29", "10:30-11:29", "11:30-12:00"]:
    sub = df[df["time_bucket"] == bucket]
    if len(sub) > 0:
        sw = sub[sub["pnl"] > 0]
        print(f"  {bucket}: {len(sub)} trades, {len(sw)}W/{len(sub)-len(sw)}L ({len(sw)/len(sub):.0%}), P&L: ${sub['pnl'].sum():+,.2f}, Avg: ${sub['pnl'].mean():+,.2f}")

# Day of week (approximate from dates)
print(f"\nSymbol Recommendations (based on P&L and consistency):")
for _, r in sym_df.iterrows():
    if r["total_pnl"] > 50:
        print(f"  KEEP  {r['symbol']}: ${r['total_pnl']:+,.2f} over {r['trades']} trades (PF={r['profit_factor']:.2f})")
    elif r["total_pnl"] > 0:
        print(f"  WATCH {r['symbol']}: ${r['total_pnl']:+,.2f} marginal, needs more data")
    else:
        print(f"  DROP  {r['symbol']}: ${r['total_pnl']:+,.2f} over {r['trades']} trades")

# ============================================================
# 7. ANNUALIZED PROJECTIONS
# ============================================================
print(f"\n{'=' * 70}")
print("7. ANNUALIZED PROJECTIONS")
print("=" * 70)

calendar_days = 25  # Mar 1-25 (with gap Mar 13-22)
# Actual trading days with trades
actual_trading_days = trading_days_in_sample
annual_trading_days = 252

# Method 1: Scale daily P&L
daily_avg_pnl = daily_pnl.mean()
annual_pnl_est = daily_avg_pnl * annual_trading_days
annual_return = annual_pnl_est / initial_equity * 100

print(f"\nSample period: {actual_trading_days} trading days, {total_trades} trades")
print(f"Daily avg P&L:           ${daily_avg_pnl:+,.2f}")
print(f"")
print(f"Annualized P&L (linear): ${annual_pnl_est:+,.2f}")
print(f"Annualized Return:       {annual_return:+.2f}%")
print(f"Annualized Sharpe:       {annualized_sharpe:.3f}")
print(f"Annualized Sortino:      {annualized_sortino:.3f}")

# Monte Carlo simulation
print(f"\nMonte Carlo Simulation (10,000 paths, 252 days):")
np.random.seed(42)
n_sims = 10000
annual_pnls = []
for _ in range(n_sims):
    # Resample daily P&L with replacement
    sim_days = np.random.choice(daily_pnl.values, size=annual_trading_days, replace=True)
    annual_pnls.append(sim_days.sum())
annual_pnls = np.array(annual_pnls)

print(f"  Median annual P&L:     ${np.median(annual_pnls):+,.2f}")
print(f"  Mean annual P&L:       ${np.mean(annual_pnls):+,.2f}")
print(f"  5th percentile:        ${np.percentile(annual_pnls, 5):+,.2f}")
print(f"  25th percentile:       ${np.percentile(annual_pnls, 25):+,.2f}")
print(f"  75th percentile:       ${np.percentile(annual_pnls, 75):+,.2f}")
print(f"  95th percentile:       ${np.percentile(annual_pnls, 95):+,.2f}")
print(f"  Prob(profitable year): {(annual_pnls > 0).mean():.1%}")
print(f"  Prob(lose > $5K):      {(annual_pnls < -5000).mean():.1%}")

# Risk of ruin estimate
print(f"\nRisk Assessment:")
max_annual_dd_sims = []
for _ in range(5000):
    sim_days = np.random.choice(daily_pnl.values, size=annual_trading_days, replace=True)
    cum = np.cumsum(sim_days)
    running_max = np.maximum.accumulate(cum)
    dd = cum - running_max
    max_annual_dd_sims.append(dd.min())
max_annual_dd_sims = np.array(max_annual_dd_sims)
print(f"  Median max annual drawdown: ${np.median(max_annual_dd_sims):,.2f}")
print(f"  95th pct worst drawdown:    ${np.percentile(max_annual_dd_sims, 5):,.2f}")
print(f"  Worst simulated drawdown:   ${np.min(max_annual_dd_sims):,.2f}")

print(f"\n{'=' * 70}")
print("ANALYSIS COMPLETE")
print("=" * 70)
