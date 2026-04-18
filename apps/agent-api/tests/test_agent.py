from agent_api.config import ALLOWED_TABLES, Settings
from agent_api.schema import ChatMessage, ChatRequest


def test_allowed_tables_excludes_sensitive():
    forbidden = {"oauth_tokens", "accounts", "kill_switch_events", "kill_switch_state", "dna_approvals"}
    assert not (forbidden & set(ALLOWED_TABLES))


def test_allowed_tables_covers_core_trading_surface():
    required = {"trades", "orders", "daily_pnl", "strategy_daily_pnl", "recap_digests"}
    assert required.issubset(set(ALLOWED_TABLES))


def test_chat_request_rejects_empty_messages():
    try:
        ChatRequest(messages=[])
    except Exception as e:
        assert "at least 1" in str(e).lower() or "min_length" in str(e).lower()
        return
    raise AssertionError("empty messages should have failed validation")


def test_chat_request_rejects_too_many_turns():
    msgs = [ChatMessage(role="user", content=f"q{i}") for i in range(41)]
    try:
        ChatRequest(messages=msgs)
    except Exception:
        return
    raise AssertionError("40+ messages should have failed validation")


def test_settings_env_prefix(monkeypatch):
    monkeypatch.setenv("AGENT_DB_URL", "postgresql+psycopg2://x:y@z/db")
    monkeypatch.setenv("AGENT_ANTHROPIC_API_KEY", "sk-ant-test")
    s = Settings()  # type: ignore[call-arg]
    assert s.db_url.startswith("postgresql+psycopg2://")
    assert s.model == "claude-sonnet-4-6"
    assert s.allowed_tables == ALLOWED_TABLES
