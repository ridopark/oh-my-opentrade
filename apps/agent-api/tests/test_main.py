from dataclasses import dataclass, field
from typing import Any
from uuid import UUID, uuid4

import pytest
from fastapi.testclient import TestClient
from langchain_core.messages import AIMessage
from sqlalchemy import text
from sqlalchemy.ext.asyncio import create_async_engine

from agent_api.sessions import SessionRepo


@dataclass
class FakeGraph:
    canned: dict[str, Any]
    seen_thread_ids: list[str] = field(default_factory=list)

    async def ainvoke(self, _inputs, config=None):
        if config and "configurable" in config:
            self.seen_thread_ids.append(config["configurable"].get("thread_id", ""))
        return self.canned

    async def aget_state(self, _config):
        return None


class FakeSaver:
    async def setup(self):
        return None


class FakePool:
    def __init__(self, *args, **kwargs):
        pass

    async def open(self):
        return None

    async def close(self):
        return None


SQLITE_SCHEMA = """
CREATE TABLE chat_sessions (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL DEFAULT 'New chat',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_turn_at TIMESTAMP,
    turn_count INTEGER NOT NULL DEFAULT 0,
    deleted_at TIMESTAMP
);
"""


@pytest.fixture
def client_factory(monkeypatch):
    monkeypatch.setenv("AGENT_DB_URL", "postgresql+psycopg://u:p@h/db")
    monkeypatch.setenv("AGENT_WRITER_DB_URL", "postgresql+psycopg://u:p@h/db")
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
        fake_graph = FakeGraph(canned=canned)
        fake_bundle = agent_module.AgentBundle(graph=fake_graph, db=None)
        monkeypatch.setattr(
            main_module, "build_agent",
            lambda _s, context_builder=None, checkpointer=None: fake_bundle,
        )

        # Replace the Postgres pool + saver + engine factories with stand-ins that
        # don't actually open connections. The session repo points at an
        # in-memory SQLite. The fake saver/pool satisfy the lifespan wiring.
        engine = create_async_engine("sqlite+aiosqlite:///:memory:")

        async def _provision_schema():
            async with engine.begin() as conn:
                await conn.execute(text(SQLITE_SCHEMA))

        import asyncio
        asyncio.get_event_loop().run_until_complete(_provision_schema())

        monkeypatch.setattr(main_module, "AsyncConnectionPool", FakePool)
        monkeypatch.setattr(main_module, "AsyncPostgresSaver", lambda _pool: FakeSaver())
        monkeypatch.setattr(main_module, "make_async_engine", lambda _url: engine)

        main_module.limiter.reset()
        client = TestClient(main_module.app)
        client._fake_graph = fake_graph  # expose for assertions
        return client

    return _make


def _req(user_message="hi", session_id=None):
    body = {"user_message": user_message}
    if session_id is not None:
        body["session_id"] = str(session_id)
    return body


def test_chat_creates_session_when_id_absent(client_factory):
    with client_factory() as client:
        r = client.post("/chat", json=_req())
        assert r.status_code == 200
        body = r.json()
        assert body["answer"] == "42"
        assert body["kind"] == "factual"
        assert body["prompt_version"] == "v1"
        assert body["created_session"] is True
        UUID(body["session_id"])  # valid uuid
        assert client._fake_graph.seen_thread_ids[-1] == body["session_id"]


def test_chat_401_on_mismatched_secret(client_factory):
    with client_factory(proxy_secret="s3cret") as client:
        r = client.post("/chat", json=_req(), headers={"X-Proxy-Secret": "WRONG"})
        assert r.status_code == 401


def test_chat_200_on_matched_secret(client_factory):
    with client_factory(proxy_secret="s3cret") as client:
        r = client.post("/chat", json=_req(), headers={"X-Proxy-Secret": "s3cret"})
        assert r.status_code == 200


def test_chat_404_on_unknown_session_id(client_factory):
    with client_factory() as client:
        r = client.post("/chat", json=_req(session_id=uuid4()))
        assert r.status_code == 404


def test_chat_second_turn_reuses_session_id(client_factory):
    with client_factory() as client:
        first = client.post("/chat", json=_req()).json()
        session_id = first["session_id"]
        second = client.post("/chat", json=_req("follow up", session_id=session_id)).json()
        assert second["session_id"] == session_id
        assert second["created_session"] is False
        assert client._fake_graph.seen_thread_ids[-1] == session_id


def test_chat_rate_limit_returns_429(client_factory):
    with client_factory(rate_limit="3/minute") as client:
        for _ in range(3):
            assert client.post("/chat", json=_req()).status_code == 200
        r = client.post("/chat", json=_req())
        assert r.status_code == 429


def test_sessions_list_then_rename_then_delete(client_factory):
    with client_factory() as client:
        first = client.post("/chat", json=_req()).json()
        sid = first["session_id"]

        listed = client.get("/sessions").json()
        assert any(s["id"] == sid for s in listed)

        renamed = client.patch(f"/sessions/{sid}", json={"title": "quant review"}).json()
        assert renamed["title"] == "quant review"

        deleted = client.delete(f"/sessions/{sid}")
        assert deleted.status_code == 204

        after = client.get("/sessions").json()
        assert all(s["id"] != sid for s in after)


def test_get_session_returns_detail_with_empty_messages(client_factory):
    with client_factory() as client:
        sid = client.post("/chat", json=_req()).json()["session_id"]
        detail = client.get(f"/sessions/{sid}").json()
        assert detail["id"] == sid
        # FakeGraph.aget_state returns None, so messages is empty — proves the
        # handler degrades gracefully when no checkpoint state exists.
        assert detail["messages"] == []
