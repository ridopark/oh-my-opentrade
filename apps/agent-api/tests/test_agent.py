from langchain_core.messages import AIMessage, HumanMessage

from agent_api.config import ALLOWED_TABLES, Settings
from agent_api.main import extract_quant_answer, parse_agent_result
from agent_api.prompts import PROMPT_VERSION
from agent_api.schema import ChatMessage, ChatRequest, QuantAnswer


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


def test_extract_quant_answer_structured_response_safety_net():
    # response_format is not wired today, but if a LangGraph upgrade
    # re-populates structured_response we still honor it.
    quant = QuantAnswer(kind="analysis", answer="MACD PF is 1.3", evidence=["SELECT 1"])
    result = {"messages": [], "structured_response": quant}
    out, source = extract_quant_answer(result)
    assert source == "structured"
    assert out.kind == "analysis"
    assert out.evidence == ["SELECT 1"]


def test_extract_quant_answer_json_block_tail():
    raw = (
        "There are 579 trades total.\n\n"
        '```json\n{"kind":"factual","answer":"579 trades","evidence":[]}\n```'
    )
    result = {"messages": [AIMessage(content=raw)]}
    out, source = extract_quant_answer(result)
    assert source == "json_block"
    assert out.kind == "factual"
    assert "579" in out.answer


def test_extract_quant_answer_plain_when_no_block():
    result = {"messages": [AIMessage(content="just 579")]}
    out, source = extract_quant_answer(result)
    assert source == "plain"
    assert out.kind == "factual"
    assert out.answer == "just 579"
    assert out.evidence == []


def test_extract_quant_answer_mid_body_block_is_ignored():
    # A JSON-looking fence in the middle of the response (e.g. the model
    # quoting a prior tool result) must not be mistaken for the classifier.
    raw = (
        "Here is the row I found:\n"
        '```json\n{"symbol":"AAPL","qty":100}\n```\n'
        "So the answer is AAPL."
    )
    result = {"messages": [AIMessage(content=raw)]}
    out, source = extract_quant_answer(result)
    assert source == "plain"
    assert "AAPL" in out.answer


def test_extract_quant_answer_invalid_json_falls_back_to_plain():
    raw = 'An answer.\n```json\n{kind: factual, broken}\n```'
    result = {"messages": [AIMessage(content=raw)]}
    out, source = extract_quant_answer(result)
    assert source == "plain"
    assert out.kind == "factual"


def test_extract_quant_answer_missing_required_field_falls_back():
    raw = 'The answer.\n```json\n{"kind":"analysis"}\n```'
    result = {"messages": [AIMessage(content=raw)]}
    out, source = extract_quant_answer(result)
    assert source == "plain"


def test_extract_quant_answer_downgrades_unsubstantiated_recommendation():
    raw = (
        "You should stop trading.\n"
        '```json\n{"kind":"recommendation","answer":"Stop.","evidence":[]}\n```'
    )
    result = {"messages": [AIMessage(content=raw)]}
    out, source = extract_quant_answer(result)
    assert source == "json_block"
    assert out.kind == "analysis"


def test_prompt_version_is_v1():
    assert PROMPT_VERSION == "v1"
