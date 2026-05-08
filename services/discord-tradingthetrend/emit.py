"""HTTP emit to omo-core /internal/tradingthetrend/signal."""

from __future__ import annotations

from dataclasses import asdict, dataclass

import httpx


@dataclass
class SignalPayload:
    signal_id: str         # "tradingthetrend:<message_id>:<line_index>"
    message_id: str
    author: str
    posted_at: str         # ISO8601 UTC
    ticker: str
    right: str             # C | P
    strike: float
    trigger: float
    raw_line: str


class Emitter:
    def __init__(self, backend_url: str, secret: str, *, timeout: float = 5.0) -> None:
        self._url = backend_url.rstrip("/") + "/internal/tradingthetrend/signal"
        self._headers = {
            "Content-Type": "application/json",
            "X-TradingTheTrend-Secret": secret,
        }
        self._client = httpx.AsyncClient(timeout=timeout)

    async def emit(self, payload: SignalPayload) -> tuple[int, str]:
        resp = await self._client.post(self._url, json=asdict(payload), headers=self._headers)
        return resp.status_code, resp.text

    async def close(self) -> None:
        await self._client.aclose()
