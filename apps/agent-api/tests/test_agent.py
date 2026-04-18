from langchain_core.messages import AIMessage, HumanMessage

from agent_api.config import ALLOWED_TABLES, Settings
from agent_api.main import parse_agent_result
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
    assert s.rate_limit == "20/minute"


def test_parse_agent_result_string_content():
    result = {
        "messages": [
            HumanMessage(content="how many trades?"),
            AIMessage(content="There are 579 trades."),
        ]
    }
    answer, sql = parse_agent_result(result)
    assert answer == "There are 579 trades."
    assert sql == []


def test_parse_agent_result_extracts_sql_from_tool_calls():
    tool_call = {"name": "sql_db_query", "args": {"query": "SELECT COUNT(*) FROM trades"}, "id": "c1"}
    result = {
        "messages": [
            HumanMessage(content="how many trades?"),
            AIMessage(content="", tool_calls=[tool_call]),
            AIMessage(content="There are 579 trades."),
        ]
    }
    answer, sql = parse_agent_result(result)
    assert answer == "There are 579 trades."
    assert sql == ["SELECT COUNT(*) FROM trades"]


def test_parse_agent_result_ignores_non_sql_tool_calls():
    result = {
        "messages": [
            AIMessage(content="", tool_calls=[
                {"name": "sql_db_schema", "args": {"table_names": "trades"}, "id": "c1"},
                {"name": "sql_db_query", "args": {"query": "SELECT 1"}, "id": "c2"},
            ]),
            AIMessage(content="done"),
        ]
    }
    _, sql = parse_agent_result(result)
    assert sql == ["SELECT 1"]


def test_parse_agent_result_list_content_blocks():
    result = {
        "messages": [
            AIMessage(content=[
                {"type": "text", "text": "Part one."},
                {"type": "text", "text": "Part two."},
                {"type": "tool_use", "id": "x", "name": "sql_db_query", "input": {"query": "SELECT 1"}},
            ]),
        ]
    }
    answer, _ = parse_agent_result(result)
    assert "Part one." in answer and "Part two." in answer


def test_parse_agent_result_takes_last_answer():
    result = {
        "messages": [
            AIMessage(content="first draft"),
            AIMessage(content="final"),
        ]
    }
    answer, _ = parse_agent_result(result)
    assert answer == "final"


def test_parse_agent_result_empty_when_no_ai_text():
    result = {
        "messages": [
            HumanMessage(content="q"),
            AIMessage(content="", tool_calls=[{"name": "sql_db_query", "args": {"query": "SELECT 1"}, "id": "c1"}]),
        ]
    }
    answer, sql = parse_agent_result(result)
    assert answer == ""
    assert sql == ["SELECT 1"]
