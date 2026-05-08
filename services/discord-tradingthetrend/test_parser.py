"""Parser tests for the TradingTheTrend grammar.

Sample lines come from the channel's morning watchlist posts. Grammar is
strict (TICKER STRIKE[c|p] > TRIGGER); commentary and malformed lines must
be silently skipped.
"""

from __future__ import annotations

import pytest

from parser import ParsedSignal, parse_message


def _one(text: str) -> ParsedSignal:
    sigs = parse_message(text)
    assert len(sigs) == 1, f"expected 1 signal, got {len(sigs)}: {sigs!r}"
    return sigs[0]


def _none(text: str) -> None:
    assert parse_message(text) == []


# ---------------------------------------------------------------------------
# Sample lines from the design-review message
# ---------------------------------------------------------------------------

def test_sample_rklb_call():
    s = _one("RKLB    90c     >    88.00")
    assert s.ticker == "RKLB"
    assert s.right == "C"
    assert s.strike == 90.0
    assert s.trigger == 88.00


def test_sample_msft_call():
    s = _one("MSFT 425c > 423.00")
    assert s.ticker == "MSFT"
    assert s.strike == 425.0
    assert s.trigger == 423.00


def test_sample_nvda_decimal_strike():
    s = _one("NVDA 217.5c > 215.00")
    assert s.strike == 217.5
    assert s.right == "C"


def test_sample_tsla_call():
    s = _one("TSLA 425c > 421.00")
    assert s.trigger == 421.00


def test_multiline_full_watchlist():
    msg = (
        "RKLB 90c > 88.00\n"
        "MSFT 425c > 423.00\n"
        "NVDA 217.5c > 215.00\n"
        "TSLA 425c > 421.00\n"
    )
    sigs = parse_message(msg)
    assert len(sigs) == 4
    assert [s.ticker for s in sigs] == ["RKLB", "MSFT", "NVDA", "TSLA"]


# ---------------------------------------------------------------------------
# Adversarial / formatting variants the grammar must tolerate
# ---------------------------------------------------------------------------

def test_lowercase_right_uppercases():
    s = _one("AAPL 150c > 148.00")
    assert s.right == "C"


def test_uppercase_right_passes():
    s = _one("AAPL 150C > 148.00")
    assert s.right == "C"


def test_lowercase_ticker_normalized():
    s = _one("aapl 150c > 148.00")
    assert s.ticker == "AAPL"


def test_put_right():
    s = _one("SPY 500p > 498.00")
    assert s.right == "P"


def test_uppercase_put_right():
    s = _one("SPY 500P > 498.00")
    assert s.right == "P"


def test_decimal_trigger():
    s = _one("AAPL 150c > 148.55")
    assert s.trigger == 148.55


def test_integer_trigger():
    s = _one("AAPL 150c > 148")
    assert s.trigger == 148.0


def test_decimal_strike_and_trigger():
    s = _one("AAPL 150.5c > 148.25")
    assert s.strike == 150.5
    assert s.trigger == 148.25


def test_extra_whitespace():
    s = _one("   AAPL    150c   >    148.00   ")
    assert s.ticker == "AAPL"
    assert s.trigger == 148.00


def test_no_space_around_gt():
    s = _one("AAPL 150c>148.00")
    assert s.trigger == 148.00


def test_six_letter_ticker():
    s = _one("GOOGL 200c > 198.00")
    assert s.ticker == "GOOGL"


def test_raw_line_preserved():
    s = _one("RKLB 90c > 88.00")
    assert s.raw_line == "RKLB 90c > 88.00"


# ---------------------------------------------------------------------------
# Noise / commentary lines that must NOT parse
# ---------------------------------------------------------------------------

@pytest.mark.parametrize(
    "text",
    [
        # Commentary
        "Watching RKLB closely today",
        "Big move on NVDA",
        # Empty / whitespace
        "",
        "   ",
        "\t\n",
        # Missing right letter
        "RKLB 90 > 88.00",
        # Missing > separator
        "RKLB 90c 88.00",
        # Backwards comparison (not the grammar)
        "RKLB 90c < 88.00",
        # Action prefix from the OTHER channel grammar — must NOT match here
        "BTO RKLB 4/27 90c @ 1.50",
        "STC NVDA 4/27 205c @ 2.03",
        # Negative numbers
        "RKLB -90c > 88.00",
        "RKLB 90c > -88.00",
        # Wrong right letter
        "RKLB 90x > 88.00",
        # Extra tokens
        "RKLB 90c > 88.00 partial",
    ],
)
def test_noise_lines_skipped(text):
    _none(text)


def test_pure_noise_block():
    msg = "Hey traders!\n\nNothing today\n\nSee you tomorrow"
    assert parse_message(msg) == []


def test_mixed_signal_and_commentary():
    # First line parses, second is commentary, third parses.
    msg = (
        "RKLB 90c > 88.00\n"
        "Watching this one closely\n"
        "MSFT 425c > 423.00\n"
    )
    sigs = parse_message(msg)
    assert len(sigs) == 2
    assert sigs[0].ticker == "RKLB"
    assert sigs[1].ticker == "MSFT"


def test_returns_list_not_single():
    assert isinstance(parse_message("RKLB 90c > 88.00"), list)


def test_empty_string_returns_empty_list():
    assert parse_message("") == []
