"""Parse TradingTheTrend Discord watchlist lines into structured signals.

Grammar (per-line, case-insensitive):
    TICKER STRIKE(C|P) > TRIGGER

Examples:
    RKLB 90c > 88.00         long call: buy when underlying breaks above 88.00
    MSFT 425c >  423.00
    NVDA 217.5c > 215.00     decimal strike OK
    TSLA 425p > 421.00       puts use right=P

The grammar carries no expiry, no fill price, and no exit info. Those are
strategy-level concerns:
- expiry resolves to nearest weekly Friday (configurable on the Go side).
- entry uses break-and-retest on the underlying (TRIGGER is the breakout level).
- exits are mechanical (chandelier, premium hard-stop, EOD flatten).

Lines that don't match the grammar (commentary, blanks, malformed) are
silently skipped — same convention as the copytrade parser.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

_LINE_RE = re.compile(
    r"""
    ^\s*
    (?P<ticker>[A-Za-z]{1,6})\s+
    (?P<strike>\d+(?:\.\d+)?)
    (?P<right>[CcPp])
    \s*(?P<direction>[<>])\s*
    (?P<trigger>\d+(?:\.\d+)?)
    \s*$
    """,
    re.VERBOSE,
)


@dataclass(frozen=True)
class ParsedSignal:
    ticker: str    # uppercase
    right: str     # C | P (uppercase)
    strike: float
    trigger: float
    raw_line: str


def parse_message(text: str) -> list[ParsedSignal]:
    """Parse a (potentially multi-line) Discord message body.

    Returns an empty list for pure commentary or noise.

    Valid grammar:
        TICKER STRIKE C > TRIGGER  (call breakout, long-directional)
        TICKER STRIKE P < TRIGGER  (put breakdown, short-directional)
    Inconsistent direction (C < or P >) is silently skipped.
    """
    out: list[ParsedSignal] = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            continue
        m = _LINE_RE.match(line)
        if not m:
            continue
        right = m.group("right").upper()
        direction = m.group("direction")
        if (right == "C" and direction != ">") or (right == "P" and direction != "<"):
            continue
        out.append(
            ParsedSignal(
                ticker=m.group("ticker").upper(),
                right=right,
                strike=float(m.group("strike")),
                trigger=float(m.group("trigger")),
                raw_line=line,
            )
        )
    return out
