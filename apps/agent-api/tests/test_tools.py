import json

import httpx
import pytest

from agent_api.tools import WindowError, make_performance_tools, resolve_window


def test_resolve_window_rolling_days():
    from datetime import datetime

    frm, to = resolve_window("7d")
    assert frm < to
    d_from = datetime.fromisoformat(frm)
    d_to = datetime.fromisoformat(to)
    assert 604700 <= (d_to - d_from).total_seconds() <= 604900


def test_resolve_window_ytd():
    from datetime import datetime

    frm, to = resolve_window("ytd")
    d_from = datetime.fromisoformat(frm)
    d_to = datetime.fromisoformat(to)
    assert d_from.month == 1 and d_from.day == 1
    assert d_from.year == d_to.year


def test_resolve_window_explicit_range():
    frm, to = resolve_window("2026-01-01..2026-02-01")
    assert frm.startswith("2026-01-01")
    assert to.startswith("2026-02-01")


def test_resolve_window_invalid_format():
    with pytest.raises(WindowError):
        resolve_window("last week")


def test_resolve_window_invalid_explicit_range():
    with pytest.raises(WindowError):
        resolve_window("notadate..2026-02-01")


def _build_mock_tools(handler):
    transport = httpx.MockTransport(handler)
    from agent_api import tools as tools_mod

    original_ctor = tools_mod.httpx.AsyncClient

    def _ctor(*args, **kwargs):
        kwargs.pop("timeout", None)
        return original_ctor(transport=transport, timeout=5.0)

    tools_mod.httpx.AsyncClient = _ctor  # type: ignore[attr-defined]
    try:
        return make_performance_tools("http://test-omo-core:8080")
    finally:
        tools_mod.httpx.AsyncClient = original_ctor  # type: ignore[attr-defined]


@pytest.mark.asyncio
async def test_get_strategy_performance_hits_correct_url_and_returns_body():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["url"] = str(request.url)
        return httpx.Response(200, content=b'[{"strategy":"avwap_v4","profit_factor":58.1}]')

    tools = _build_mock_tools(handler)
    perf = next(t for t in tools if t.name == "get_strategy_performance")
    out = await perf.ainvoke({"window": "30d"})
    assert "http://test-omo-core:8080/performance/strategies" in seen["url"]
    assert "from=" in seen["url"] and "to=" in seen["url"]
    assert '"strategy":"avwap_v4"' in out


@pytest.mark.asyncio
async def test_get_performance_dashboard_passes_strategy_param():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["url"] = str(request.url)
        return httpx.Response(200, content=b'{"summary":{"profit_factor":1.3}}')

    tools = _build_mock_tools(handler)
    dash = next(t for t in tools if t.name == "get_performance_dashboard")
    out = await dash.ainvoke({"window": "7d", "strategy": "macd_only_v1"})
    assert "strategy=macd_only_v1" in seen["url"]
    assert '"profit_factor":1.3' in out


@pytest.mark.asyncio
async def test_get_trades_passes_filters_and_caps_limit():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["url"] = str(request.url)
        return httpx.Response(200, content=b'{"items":[]}')

    tools = _build_mock_tools(handler)
    trades = next(t for t in tools if t.name == "get_trades")
    await trades.ainvoke({"window": "7d", "symbol": "AAPL", "limit": 1000})
    assert "symbol=AAPL" in seen["url"]
    assert "limit=200" in seen["url"]


@pytest.mark.asyncio
async def test_http_error_is_returned_as_tool_output():
    def handler(_request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, content=b"boom")

    tools = _build_mock_tools(handler)
    perf = next(t for t in tools if t.name == "get_strategy_performance")
    out = await perf.ainvoke({"window": "30d"})
    payload = json.loads(out)
    assert payload["error"].startswith("omo-core returned non-200")
    assert payload["status"] == 500


@pytest.mark.asyncio
async def test_invalid_window_short_circuits_before_http():
    called = {"n": 0}

    def handler(_request: httpx.Request) -> httpx.Response:
        called["n"] += 1
        return httpx.Response(200, content=b"{}")

    tools = _build_mock_tools(handler)
    perf = next(t for t in tools if t.name == "get_strategy_performance")
    out = await perf.ainvoke({"window": "whenever"})
    assert "unknown window" in out
    assert called["n"] == 0
