from __future__ import annotations

import re
from datetime import UTC, datetime, timedelta

import httpx
from langchain_core.tools import tool

_WINDOW_RE = re.compile(r"^(\d+)([dwmy])$")


class WindowError(ValueError):
    pass


def resolve_window(window: str) -> tuple[str, str]:
    """Translate an agent-facing window into (from_iso, to_iso) RFC3339 dates.

    Accepts:
      - `7d`, `30d`, `90d`, `180d`, `1y` — rolling lookback from now.
      - `ytd` — start of current UTC year through now.
      - `YYYY-MM-DD..YYYY-MM-DD` — explicit range.

    Month and year units are calendar-approximated: `m` = 30 days, `y` = 365
    days. Use an explicit range if you need exact month-end or leap-year math.
    """
    now = datetime.now(UTC).replace(microsecond=0)
    w = window.strip().lower()

    if w == "ytd":
        start = datetime(now.year, 1, 1, tzinfo=UTC)
        return start.isoformat(), now.isoformat()

    if ".." in w:
        left, right = w.split("..", 1)
        try:
            frm = datetime.fromisoformat(left)
            to = datetime.fromisoformat(right)
        except ValueError as e:
            raise WindowError(f"invalid explicit window '{window}': {e}") from e
        if frm.tzinfo is None:
            frm = frm.replace(tzinfo=UTC)
        if to.tzinfo is None:
            to = to.replace(tzinfo=UTC)
        return frm.isoformat(), to.isoformat()

    m = _WINDOW_RE.match(w)
    if not m:
        raise WindowError(
            f"unknown window '{window}'. Use e.g. '7d', '30d', '90d', '1y', 'ytd', or 'YYYY-MM-DD..YYYY-MM-DD'"
        )
    n, unit = int(m.group(1)), m.group(2)
    days_per_unit = {"d": 1, "w": 7, "m": 30, "y": 365}
    start = now - timedelta(days=n * days_per_unit[unit])
    return start.isoformat(), now.isoformat()


def make_performance_tools(omo_core_url: str, timeout: float = 5.0) -> list:
    base_url = omo_core_url.rstrip("/")
    client = httpx.AsyncClient(timeout=timeout)

    async def _get(path: str, params: dict) -> str:
        try:
            resp = await client.get(f"{base_url}{path}", params=params)
            resp.raise_for_status()
        except httpx.HTTPStatusError as e:
            return f"error: omo-core returned {e.response.status_code} for {path}"
        except httpx.HTTPError as e:
            return f"error: omo-core request failed ({type(e).__name__}): {e}"
        return resp.text

    @tool
    async def get_strategy_performance(window: str = "30d") -> str:
        """Return per-strategy performance metrics (PF, outlier_removed_pf, win_rate, trade counts, realized P&L, gross profit/loss) for the given window.

        Prefer this over hand-rolled SQL when comparing strategies or evaluating
        whether a strategy is outlier-dependent. Window: '7d', '30d', '90d',
        '1y', 'ytd', or 'YYYY-MM-DD..YYYY-MM-DD'.
        """
        try:
            frm, to = resolve_window(window)
        except WindowError as e:
            return f"error: {e}"
        return await _get("/performance/strategies", {"from": frm, "to": to})

    @tool
    async def get_performance_dashboard(window: str = "30d", strategy: str | None = None) -> str:
        """Return the portfolio-level (or per-strategy) performance dashboard: summary (PF, Sharpe, Sortino, Expectancy, CAGR, MaxDrawdownPct), equity curve, daily P&L, and drawdown curve.

        Pass `strategy` to scope to one strategy (e.g. `avwap_v4`); omit for
        portfolio-wide metrics. Window format same as get_strategy_performance.
        """
        try:
            frm, to = resolve_window(window)
        except WindowError as e:
            return f"error: {e}"
        params = {"from": frm, "to": to}
        if strategy:
            params["strategy"] = strategy
        return await _get("/performance/dashboard", params)

    @tool
    async def get_trades(window: str = "7d", strategy: str | None = None, symbol: str | None = None, limit: int = 50) -> str:
        """Return individual trade fills (time, symbol, side, qty, price, commission) for the window. Page size limited to 200.

        Use this when you need trade-level granularity the aggregated endpoints
        don't expose (e.g. slippage audits, specific symbol forensics).
        """
        try:
            frm, to = resolve_window(window)
        except WindowError as e:
            return f"error: {e}"
        params: dict = {"from": frm, "to": to, "limit": max(1, min(int(limit), 200))}
        if strategy:
            params["strategy"] = strategy
        if symbol:
            params["symbol"] = symbol
        return await _get("/performance/trades", params)

    return [get_strategy_performance, get_performance_dashboard, get_trades]
