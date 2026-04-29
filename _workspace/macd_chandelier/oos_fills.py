#!/usr/bin/env python3
"""Extract per-trade OOS fills restricted to 2026-04-21 and attribute exit rule.
Also cross-references the 6 MAX_HOLDING_TIME losers from the baseline run to
see which are caught earlier by the variant.

Usage: oos_fills.py <baseline_OOS.json> <variant_OOS.json>
"""
import json, re, sys
from collections import defaultdict

def extract_exits(path, date_prefix='2026-04-21'):
    d = json.load(open(path))
    out = []
    for t in d['trades']:
        if not t.get('is_exit'): continue
        ts = t.get('filled_at','')
        if not ts.startswith(date_prefix): continue
        sym = t.get('symbol','')
        underlying = sym[:6].rstrip('0123456789')  # rough OCC underlying extraction
        # Better: OCC is e.g. RIVN260116C00020000 — root is letters before 6-digit YYMMDD
        m = re.match(r'^([A-Z]+)\d{6}[CP]\d+', sym)
        if m: underlying = m.group(1)
        rule = 'UNKNOWN'
        rm = re.search(r'exit_monitor:([A-Z_]+)', t.get('rationale','') or '')
        if rm: rule = rm.group(1)
        out.append({
            'filled_at': ts,
            'symbol': sym,
            'underlying': underlying,
            'quantity': t.get('quantity',0),
            'exit_price': t.get('price',0),
            'pnl': t.get('pnl',0),
            'rule': rule,
            'mfe': t.get('premium_mfe_pct'),
            'mae': t.get('premium_mae_pct'),
            'minutes_held': t.get('minutes_held'),
            'rationale_short': (t.get('rationale','') or '')[:140],
        })
    return out

def match_trades(base_exits, var_exits):
    """Pair baseline and variant exits by underlying on 2026-04-21.
    There may be multiple positions per underlying; pair in order by entry time.
    """
    def by_underlying(exits):
        g = defaultdict(list)
        for e in exits: g[e['underlying']].append(e)
        for k in g: g[k].sort(key=lambda x: x['filled_at'])
        return g
    bg = by_underlying(base_exits)
    vg = by_underlying(var_exits)
    keys = sorted(set(bg) | set(vg))
    rows = []
    for k in keys:
        b_list = bg.get(k,[])
        v_list = vg.get(k,[])
        # pair up to min length
        for i in range(max(len(b_list), len(v_list))):
            b = b_list[i] if i<len(b_list) else None
            v = v_list[i] if i<len(v_list) else None
            rows.append((k, b, v))
    return rows

def main():
    base_path = sys.argv[1]
    var_path  = sys.argv[2]
    base_ex = extract_exits(base_path)
    var_ex  = extract_exits(var_path)

    # Baseline MAX_HOLDING losers on 04-21
    losers = [e for e in base_ex if e['rule']=='MAX_HOLDING_TIME' and (e['pnl'] or 0) < 0]
    print(f"Baseline 04-21: {len(base_ex)} exits, {len(losers)} MAX_HOLDING_TIME losers")
    for l in losers:
        print(f"  {l['filled_at']} {l['underlying']:6s} sym={l['symbol']:<22} pnl=${l['pnl']:>9,.0f}  mfe={l['mfe']} mae={l['mae']} held={l['minutes_held']}m")
    print()

    print(f"Variant 04-21: {len(var_ex)} exits")
    by_rule = defaultdict(list)
    for e in var_ex: by_rule[e['rule']].append(e)
    for rule, lst in sorted(by_rule.items(), key=lambda kv: -sum((x['pnl'] or 0) for x in kv[1])):
        pnl_sum = sum(x['pnl'] or 0 for x in lst)
        print(f"  {rule:20s} n={len(lst):3d} PnL=${pnl_sum:>9,.0f}")
    print()

    print("Paired by underlying (baseline -> variant):")
    print("  underlying | baseline-rule  baseline-pnl | variant-rule   variant-pnl | delta")
    rows = match_trades(base_ex, var_ex)
    for k, b, v in rows:
        br = b['rule'] if b else '---'
        bp = b['pnl'] if b else 0
        vr = v['rule'] if v else '---'
        vp = v['pnl'] if v else 0
        delta = (vp or 0) - (bp or 0)
        flag = ''
        if b and b['rule']=='MAX_HOLDING_TIME' and (b['pnl'] or 0)<0:
            flag = ' <-- one of the 6 losers'
            if v and v['rule']=='CHANDELIER_TRAIL':
                flag += ' [CAUGHT BY CHANDELIER]'
        print(f"  {k:6s} | {br:14s} ${bp or 0:>9,.0f} | {vr:14s} ${vp or 0:>9,.0f} | ${delta:>+9,.0f}{flag}")

if __name__=='__main__':
    main()
