import time

from agent_api.context import ContextBuilder


def test_cache_returns_same_value_within_ttl():
    calls = {"n": 0}

    def fetch():
        calls["n"] += 1
        return f"block-{calls['n']}"

    cb = ContextBuilder(fetcher=fetch, ttl_seconds=60.0)
    first = cb.build()
    second = cb.build()
    assert first == second == "block-1"
    assert calls["n"] == 1


def test_cache_refetches_after_ttl():
    calls = {"n": 0}

    def fetch():
        calls["n"] += 1
        return f"block-{calls['n']}"

    cb = ContextBuilder(fetcher=fetch, ttl_seconds=0.01)
    cb.build()
    time.sleep(0.02)
    cb.build()
    assert calls["n"] == 2


def test_fetcher_error_returns_empty_string():
    def fetch():
        raise RuntimeError("db down")

    cb = ContextBuilder(fetcher=fetch, ttl_seconds=60.0)
    assert cb.build() == ""


def test_fetcher_error_is_cached_to_avoid_hot_retry():
    calls = {"n": 0}

    def fetch():
        calls["n"] += 1
        raise RuntimeError("db down")

    cb = ContextBuilder(fetcher=fetch, ttl_seconds=60.0, error_ttl_seconds=60.0)
    cb.build()
    cb.build()
    cb.build()
    assert calls["n"] == 1


def test_error_ttl_is_shorter_than_success_ttl():
    calls = {"n": 0}

    def fetch():
        calls["n"] += 1
        raise RuntimeError("db down")

    cb = ContextBuilder(fetcher=fetch, ttl_seconds=300.0, error_ttl_seconds=0.01)
    cb.build()
    time.sleep(0.02)
    cb.build()
    assert calls["n"] == 2


def test_success_uses_long_ttl_even_after_error_window():
    results = iter(["block-ok", "block-second"])
    calls = {"n": 0}

    def fetch():
        calls["n"] += 1
        return next(results)

    cb = ContextBuilder(fetcher=fetch, ttl_seconds=60.0, error_ttl_seconds=0.01)
    first = cb.build()
    time.sleep(0.02)
    second = cb.build()
    assert first == second == "block-ok"
    assert calls["n"] == 1
