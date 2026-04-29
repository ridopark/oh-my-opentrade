#!/usr/bin/env python3
"""Summarize a macd_chandelier backtest result file.
Usage: analyze.py <results.json> [<label>]
"""
import json, re, sys
from collections import defaultdict

def rule_of(t):
    r = t.get('rationale') or ''
    m = re.search(r'exit_monitor:([A-Z_]+)', r)
    if m: return m.group(1)
    return 'UNKNOWN'

def analyze(path, label=None):
    d = json.load(open(path))
    label = label or path.rsplit('/',1)[-1].replace('_results.json','')
    trades = d.get('trades') or []
    exits = [t for t in trades if t.get('is_exit')]
    bucket_pnl = defaultdict(float)
    bucket_n   = defaultdict(int)
    bucket_wins= defaultdict(int)
    bucket_losses=defaultdict(int)
    for t in exits:
        rule = rule_of(t)
        pnl = t.get('pnl',0) or 0
        bucket_pnl[rule] += pnl
        bucket_n[rule]   += 1
        if pnl>0: bucket_wins[rule] += 1
        elif pnl<0: bucket_losses[rule] += 1
    pf = d.get('profit_factor',0) or 0
    wr = d.get('win_rate_pct',0) or 0
    pnl_tot = d.get('total_pnl',0) or 0
    tc = d.get('trade_count',0) or 0
    dd = d.get('max_drawdown_pct',0) or 0
    sh = d.get('sharpe_ratio',0) or 0
    print(f"[{label}] PF={pf:.3f} WR={wr:.1f}% PnL=${pnl_tot:,.0f} Trades={tc} DD={dd:.2f}% Sharpe={sh:.2f}")
    print(f"  exit breakdown ({len(exits)} exits):")
    for rule in sorted(bucket_n, key=lambda r: -bucket_n[r]):
        n=bucket_n[rule]; w=bucket_wins[rule]; l=bucket_losses[rule]
        p=bucket_pnl[rule]
        print(f"    {rule:20s} n={n:4d} W={w:3d} L={l:3d} PnL=${p:>9,.0f}")
    return {'label':label,'pf':pf,'wr':wr,'pnl':pnl_tot,'trades':tc,'dd':dd,'sharpe':sh,
            'bucket_n':dict(bucket_n),'bucket_pnl':dict(bucket_pnl)}

if __name__=='__main__':
    analyze(sys.argv[1], sys.argv[2] if len(sys.argv)>2 else None)
