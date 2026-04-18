from dataclasses import dataclass
from typing import Any

import pytest
from fastapi.testclient import TestClient
from langchain_core.messages import AIMessage


@dataclass
class FakeGraph:
    canned: dict[str, Any]

    async def ainvoke(self, _inputs, config=None):
        return self.canned


@pytest.fixture
def client_factory(monkeypatch):
    monkeypatch.setenv("AGENT_DB_URL", "postgresql+psycopg2://u:p@h/db")
    monkeypatch.setenv("AGENT_ANTHROPIC_API_KEY", "sk-ant-test")

    def _make(proxy_secret: str = "", rate_limit: str = "1000/minute"):
        monkeypatch.setenv("AGENT_PROXY_SHARED_SECRET", proxy_secret)
        monkeypatch.setenv("AGENT_RATE_LIMIT", rate_limit)

        from agent_api import agent as agent_module
        from agent_api import main as main_module

        from agent_api.schema import QuantAnswer

        canned = {
            "messages": [AIMessage(content="42")],
            "structured_response": QuantAnswer(kind="factual", answer="42", evidence=[]),
        }
        fake_bundle = agent_module.AgentBundle(graph=FakeGraph(canned=canned), db=None)
        monkeypatch.setattr(agent_module, "build_agent", lambda _s, context_builder=None: fake_bundle)
        monkeypatch.setattr(main_module, "build_agent", lambda _s, context_builder=None: fake_bundle)

        main_module.limiter.reset()
        return TestClient(main_module.app)

    return _make


def _msg(content="hi"):
    return {"messages": [{"role": "user", "content": content}]}


def test_chat_200_when_no_secret_configured(client_factory):
    with client_factory(proxy_secret="") as client:
        r = client.post("/chat", json=_msg())
        assert r.status_code == 200
        body = r.json()
        assert body["answer"] == "42"
        assert body["kind"] == "factual"
        assert body["prompt_version"] == "v1"


def test_chat_401_on_mismatched_secret(client_factory):
    with client_factory(proxy_secret="s3cret") as client:
        r = client.post("/chat", json=_msg(), headers={"X-Proxy-Secret": "WRONG"})
        assert r.status_code == 401


def test_chat_200_on_matched_secret(client_factory):
    with client_factory(proxy_secret="s3cret") as client:
        r = client.post("/chat", json=_msg(), headers={"X-Proxy-Secret": "s3cret"})
        assert r.status_code == 200


def test_chat_401_when_secret_configured_but_header_missing(client_factory):
    with client_factory(proxy_secret="s3cret") as client:
        r = client.post("/chat", json=_msg())
        assert r.status_code == 401


def test_chat_rate_limit_returns_429(client_factory):
    with client_factory(rate_limit="3/minute") as client:
        for _ in range(3):
            assert client.post("/chat", json=_msg()).status_code == 200
        r = client.post("/chat", json=_msg())
        assert r.status_code == 429
