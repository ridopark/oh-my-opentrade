import json
import logging
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Callable

from sqlalchemy import create_engine, text
from sqlalchemy.engine import Engine

log = logging.getLogger("agent_api")


@dataclass
class _Cached:
    block: str
    fetched_at: float


class ContextBuilder:
    def __init__(self, fetcher: Callable[[], str], ttl_seconds: float = 300.0):
        self._fetcher = fetcher
        self._ttl = ttl_seconds
        self._cached: _Cached | None = None
        self._call_count = 0

    def build(self) -> str:
        now = time.monotonic()
        if self._cached and (now - self._cached.fetched_at) < self._ttl:
            return self._cached.block
        block = self._safe_fetch()
        self._cached = _Cached(block=block, fetched_at=now)
        return block

    def _safe_fetch(self) -> str:
        self._call_count += 1
        try:
            return self._fetcher()
        except Exception as e:
            log.warning(json.dumps({"event": "context_fetch_failed", "error": str(e)}))
            return ""


def fetch_context_from_db(engine: Engine) -> str:
    lines: list[str] = []
    with engine.connect() as conn:
        pnl = conn.execute(text(
            "SELECT date, realized_pnl, trade_count, max_drawdown "
            "FROM daily_pnl ORDER BY date DESC LIMIT 1"
        )).fetchone()
        if pnl:
            lines.append(
                f"- Latest daily P&L ({pnl.date}): realized ${pnl.realized_pnl:.0f}, "
                f"{pnl.trade_count} trades, max DD ${pnl.max_drawdown:.0f}"
            )

        strategies = conn.execute(text(
            "SELECT strategy, SUM(trade_count) AS trades, SUM(realized_pnl) AS pnl "
            "FROM strategy_daily_pnl "
            "WHERE date >= CURRENT_DATE - INTERVAL '7 days' "
            "GROUP BY strategy HAVING SUM(trade_count) > 0 "
            "ORDER BY trades DESC LIMIT 10"
        )).fetchall()
        if strategies:
            parts = [f"{r.strategy} ({r.trades}t, ${r.pnl:.0f})" for r in strategies]
            lines.append("- Active strategies (last 7d): " + ", ".join(parts))

        recap = conn.execute(text(
            "SELECT digest_date, body FROM recap_digests "
            "ORDER BY digest_date DESC LIMIT 1"
        )).fetchone()
        if recap:
            body = (recap.body or "").strip().replace("\n", " ")
            if len(body) > 240:
                body = body[:237] + "..."
            lines.append(f"- Latest recap ({recap.digest_date}): {body}")

    if not lines:
        return ""
    return "## Current system context (read-only snapshot)\n" + "\n".join(lines)


def fetch_open_positions(omo_core_url: str, timeout: float = 2.0) -> str:
    url = omo_core_url.rstrip("/") + "/api/portfolio/positions"
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            if resp.status != 200:
                return ""
            payload = json.loads(resp.read().decode("utf-8"))
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError):
        return ""

    positions = payload if isinstance(payload, list) else payload.get("positions", [])
    if not positions:
        return "- Open positions: none"
    parts = []
    for p in positions[:10]:
        sym = p.get("symbol") or p.get("Symbol") or "?"
        qty = p.get("quantity") or p.get("Quantity") or 0
        parts.append(f"{sym} x{qty}")
    summary = ", ".join(parts)
    overflow = f" (+{len(positions) - 10} more)" if len(positions) > 10 else ""
    return f"- Open positions ({len(positions)}): {summary}{overflow}"


def make_fetcher(db_url: str, omo_core_url: str | None) -> Callable[[], str]:
    engine = create_engine(db_url, pool_pre_ping=True)

    def fetch() -> str:
        blocks = [fetch_context_from_db(engine)]
        if omo_core_url:
            pos = fetch_open_positions(omo_core_url)
            if pos:
                blocks.append(pos)
        merged = "\n".join(b for b in blocks if b)
        return merged

    return fetch
