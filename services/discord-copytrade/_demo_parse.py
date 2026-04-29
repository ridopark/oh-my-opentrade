"""Dump the parser's output for the raw sample paste used during design review.

Run: python3 _demo_parse.py
"""
from datetime import date
from parser import parse_message

RAW_SAMPLE = """\
TradingTheTrend
STC GOOGL 4/27 345c @ 2.52 partial
TradingTheTrend
100% from the avg down :vibe:
STC NVDA 4/27 205c @ 2.03 partial. Half out
TradingTheTrend
STC SPY 4/24 715c @ .55 stop hit, too much chop and not enough time left on these
TradingTheTrend
STC MSFT 4/24 430c @ 4.30 partial, holding a few freebies
TradingTheTrend
AVG NVDA 4/27 205c @ 1.40, added @ .98
TradingTheTrend
STC MSFT 4/24 430c @ 5.25 all out
TradingTheTrend
STC NVDA 4/27 205c @ 1.58 nice to see some green. Taking a few.
Partial
TradingTheTrend
STC NVDA 4/27 205c @ 1.73 partial
Taking more profit. 70%+ since the avg down.
If we break 203 it can really get moving
TradingTheTrend
STC MSFT 4/24 430c @ 2.50 partial taking a few here
STC MSFT 4/24 430c @ 2.75 partial taking it as it comes. Half out
TradingTheTrend
STC MSFT 4/24 430c @ 3.20 partial
mostly out now, holding a few. Also hit 425c from options-watchlist
TradingTheTrend
BTO NVDA 4/27 205c @ 2.11
TradingTheTrend
100% profit now :vibe:
TB22
STC AAPL 5/15 275c @ 8.3 partial  a few left
TradingTheTrend
BTO MSFT 4/24 430c @ 2.18
TradingTheTrend
Playing this flag BTW. Tight stop under 417
Edtrader
STC BABA 07/17 165c @5.35 partial. Like NIO, just trimming one so you know we are up.
Hate me all you want, I have my own plans and shorter term trades
BTO KWEB 06/18 32c @1.2, loading up more shorter term on these.
TradingTheTrend
STC NVDA 4/24 205C @ 1.60 stopped on the rest
TradingTheTrend
finally over 100%...

4.50 now still holding half
TB22
STC AAPL 5/15 275c @ 7.2 partial half out.
TradingTheTrend
STC AAPL 4/24 270c @ 4.70 partial. Leaving free runners
"""


def main():
    signals = parse_message(RAW_SAMPLE, today=date(2026, 4, 23))
    print(f"Parsed {len(signals)} actionable signals from the sample paste.\n")
    for i, s in enumerate(signals, 1):
        tail = f" | tail={s.tail!r}" if s.tail else ""
        print(f"{i:2d}. {s.action:3s} {s.ticker:5s} {s.expiry} "
              f"{s.strike:>6g}{s.right} @ {s.price:>5g}{tail}")


if __name__ == "__main__":
    main()
