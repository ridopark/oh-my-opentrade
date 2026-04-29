#!/usr/bin/env python3
"""Rewrite configs/strategies/macd_only_v1.toml to a specified variant.

Variants:
  baseline        — restore from backup (original, with PREMIUM_TRAIL)
  replace a g     — remove PREMIUM_TRAIL, add CHANDELIER_TRAIL(activate=a, giveback=g)
  layer   a g     — keep PREMIUM_TRAIL, add CHANDELIER_TRAIL(activate=a, giveback=g)

The baseline is the literal content of configs/backups/macd_only_v1_pre_chandelier_*.toml
(there is exactly one such backup from this session).

Usage:
  apply_variant.py baseline
  apply_variant.py replace 0.04 0.05
  apply_variant.py layer   0.04 0.05
"""
import glob, os, sys

ROOT = "/home/ridopark/src/oh-my-opentrade"
DST = f"{ROOT}/configs/strategies/macd_only_v1.toml"

def backup_path():
    backups = sorted(glob.glob(f"{ROOT}/configs/backups/macd_only_v1_pre_chandelier_*.toml"))
    if not backups:
        sys.exit("no backup file found")
    return backups[-1]

def load_baseline():
    with open(backup_path()) as f:
        return f.read()

PREMIUM_TRAIL_BLOCK = """# Trail to limit drawdown (net-negative P&L but provides DD protection)
[[exit_rules]]
type = "PREMIUM_TRAIL"
[exit_rules.params]
trail_pct = 0.12
min_activation = 0.08

"""

def chandelier_block(activate, giveback):
    return (
        "# CHANDELIER_TRAIL sweep variant\n"
        "[[exit_rules]]\n"
        'type = "CHANDELIER_TRAIL"\n'
        "[exit_rules.params]\n"
        f"activate_pct = {activate}\n"
        f"giveback_pct = {giveback}\n\n"
    )

def write_variant(variant, a=None, g=None):
    content = load_baseline()
    if variant == 'baseline':
        pass
    elif variant == 'replace':
        if PREMIUM_TRAIL_BLOCK not in content:
            sys.exit("PREMIUM_TRAIL block not found in baseline — check template")
        content = content.replace(PREMIUM_TRAIL_BLOCK, chandelier_block(a, g))
    elif variant == 'layer':
        if PREMIUM_TRAIL_BLOCK not in content:
            sys.exit("PREMIUM_TRAIL block not found in baseline — check template")
        # Insert chandelier immediately after the PREMIUM_TRAIL block
        content = content.replace(PREMIUM_TRAIL_BLOCK, PREMIUM_TRAIL_BLOCK + chandelier_block(a, g))
    else:
        sys.exit(f"unknown variant: {variant}")
    with open(DST, 'w') as f:
        f.write(content)
    print(f"wrote variant={variant} a={a} g={g}")

if __name__ == '__main__':
    if len(sys.argv) < 2:
        sys.exit("usage: apply_variant.py <baseline|replace|layer> [activate] [giveback]")
    v = sys.argv[1]
    if v == 'baseline':
        write_variant('baseline')
    else:
        a = float(sys.argv[2]); g = float(sys.argv[3])
        write_variant(v, a, g)
