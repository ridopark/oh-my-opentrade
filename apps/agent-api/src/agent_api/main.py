import asyncio
import json
import logging
import re
import time
from contextlib import asynccontextmanager
from typing import Any, Literal
from uuid import UUID

from fastapi import FastAPI, HTTPException, Header, Request
from langchain_core.messages import AIMessage, BaseMessage, HumanMessage
from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver
from psycopg_pool import AsyncConnectionPool
from slowapi import Limiter, _rate_limit_exceeded_handler
from slowapi.errors import RateLimitExceeded
from slowapi.util import get_remote_address

from .agent import build_agent
from .config import Settings, get_settings
from .context import ContextBuilder, make_fetcher
from .prompts import PROMPT_VERSION
from .schema import (
    ChatMessage,
    ChatRequest,
    ChatResponse,
    QuantAnswer,
    RenameRequest,
    SessionDetail,
    SessionSummary,
)
from .sessions import SessionRepo, make_async_engine
from .title_gen import generate_and_persist_title

TOOL_SQL_DB_QUERY = "sql_db_query"
_JSON_BLOCK_RE = re.compile(r"```(?:json)?\s*(\{[\s\S]*?\})\s*```", re.MULTILINE)

ParseSource = Literal["structured", "json_block", "plain"]

log = logging.getLogger("agent_api")
logging.basicConfig(level=logging.INFO, format="%(message)s")

limiter = Limiter(key_func=get_remote_address)


def _to_psycopg_dsn(url: str) -> str:
    # psycopg / langgraph's saver want a bare postgresql:// DSN.
    return url.replace("postgresql+psycopg2://", "postgresql://").replace(
        "postgresql+psycopg://", "postgresql://"
    )


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = get_settings()
    app.state.settings = settings

    context_builder = ContextBuilder(
        fetcher=make_fetcher(settings.db_url, settings.omo_core_url),
        ttl_seconds=settings.context_ttl_seconds,
        error_ttl_seconds=settings.context_error_ttl_seconds,
    )
    app.state.context_builder = context_builder

    writer_dsn = _to_psycopg_dsn(settings.writer_db_url)
    pool = AsyncConnectionPool(
        writer_dsn,
        min_size=1,
        max_size=5,
        kwargs={"autocommit": True, "prepare_threshold": 0},
        open=False,
    )
    await pool.open()
    saver = AsyncPostgresSaver(pool)
    await saver.setup()
    app.state.checkpointer_pool = pool
    app.state.checkpointer = saver

    app.state.session_engine = make_async_engine(settings.writer_db_url)
    app.state.session_repo = SessionRepo(app.state.session_engine)

    app.state.agent = build_agent(
        settings,
        context_builder=context_builder,
        checkpointer=saver,
    )

    log.info(json.dumps({
        "event": "startup",
        "model": settings.model,
        "prompt_version": PROMPT_VERSION,
        "tables": list(settings.allowed_tables),
    }))
    if not settings.proxy_shared_secret:
        log.warning(json.dumps({
            "event": "warn",
            "msg": "AGENT_PROXY_SHARED_SECRET is empty; /chat accepts unauthenticated requests",
        }))

    try:
        yield
    finally:
        await app.state.session_engine.dispose()
        await pool.close()


app = FastAPI(title="omo agent-api", lifespan=lifespan)
app.state.limiter = limiter
app.add_exception_handler(RateLimitExceeded, _rate_limit_exceeded_handler)


def _check_proxy_secret(settings: Settings, x_proxy_secret: str | None) -> None:
    if settings.proxy_shared_secret and x_proxy_secret != settings.proxy_shared_secret:
        raise HTTPException(status_code=401, detail="invalid proxy secret")


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
@limiter.limit(lambda: app.state.settings.rate_limit)
async def chat(request: Request, req: ChatRequest, x_proxy_secret: str | None = Header(default=None)):
    settings: Settings = app.state.settings
    _check_proxy_secret(settings, x_proxy_secret)

    repo: SessionRepo = app.state.session_repo
    created_session = False
    if req.session_id is None:
        session = await repo.create()
        session_id = session.id
        created_session = True
    else:
        session = await repo.get(req.session_id)
        if session is None:
            raise HTTPException(status_code=404, detail="session not found")
        session_id = session.id

    agent = app.state.agent
    started = time.monotonic()

    try:
        result = await agent.graph.ainvoke(
            {"messages": [HumanMessage(content=req.user_message)]},
            config={
                "recursion_limit": settings.recursion_limit,
                "configurable": {"thread_id": str(session_id)},
            },
        )
    except Exception:
        # Don't leave an orphan empty session behind if the very first turn
        # fails (LLM quota, network, etc.). Sessions created before this call
        # get rolled back; existing sessions are left alone so their history
        # survives the failure.
        if created_session:
            await repo.soft_delete(session_id)
        raise

    quant, parse_source = extract_quant_answer(result)
    _, sql_queries = parse_agent_result(result)
    duration_ms = int((time.monotonic() - started) * 1000)

    await repo.bump_turn(session_id)

    if created_session:
        asyncio.create_task(
            generate_and_persist_title(
                session_id=session_id,
                user_message=req.user_message,
                assistant_answer=quant.answer,
                anthropic_api_key=settings.anthropic_api_key,
                model=settings.title_model,
                repo=repo,
            )
        )

    if parse_source == "plain":
        log.warning(json.dumps({
            "event": "json_block_missing",
            "prompt_version": PROMPT_VERSION,
            "answer_len": len(quant.answer),
        }))
    log.info(json.dumps({
        "event": "chat",
        "session_id": str(session_id),
        "created_session": created_session,
        "duration_ms": duration_ms,
        "sql_count": len(sql_queries),
        "kind": quant.kind,
        "evidence_count": len(quant.evidence),
        "answer_len": len(quant.answer),
        "parse_source": parse_source,
        "prompt_version": PROMPT_VERSION,
    }))
    return ChatResponse(
        session_id=session_id,
        answer=quant.answer,
        kind=quant.kind,
        evidence=quant.evidence,
        sql_queries=sql_queries,
        prompt_version=PROMPT_VERSION,
        duration_ms=duration_ms,
        created_session=created_session,
    )


@app.get("/sessions", response_model=list[SessionSummary])
async def list_sessions(
    request: Request,
    limit: int = 50,
    x_proxy_secret: str | None = Header(default=None),
):
    settings: Settings = app.state.settings
    _check_proxy_secret(settings, x_proxy_secret)
    repo: SessionRepo = app.state.session_repo
    capped = max(1, min(limit, settings.session_list_limit))
    rows = await repo.list(limit=capped)
    return [_row_to_summary(r) for r in rows]


@app.get("/sessions/{session_id}", response_model=SessionDetail)
async def get_session(
    request: Request,
    session_id: UUID,
    x_proxy_secret: str | None = Header(default=None),
):
    settings: Settings = app.state.settings
    _check_proxy_secret(settings, x_proxy_secret)
    repo: SessionRepo = app.state.session_repo
    agent = app.state.agent

    async def _snap():
        try:
            return await agent.graph.aget_state(
                {"configurable": {"thread_id": str(session_id)}}
            )
        except Exception as e:
            log.warning(json.dumps({
                "event": "session_state_fetch_failed",
                "session_id": str(session_id),
                "error": str(e),
            }))
            return None

    row, snap = await asyncio.gather(repo.get(session_id), _snap())
    if row is None:
        raise HTTPException(status_code=404, detail="session not found")

    messages: list[ChatMessage] = []
    if snap and snap.values:
        messages = _snapshot_to_user_facing(snap.values.get("messages", []))

    summary = _row_to_summary(row)
    return SessionDetail(**summary.model_dump(), messages=messages)


@app.patch("/sessions/{session_id}", response_model=SessionSummary)
async def rename_session(
    request: Request,
    session_id: UUID,
    body: RenameRequest,
    x_proxy_secret: str | None = Header(default=None),
):
    settings: Settings = app.state.settings
    _check_proxy_secret(settings, x_proxy_secret)
    repo: SessionRepo = app.state.session_repo
    row = await repo.rename(session_id, body.title.strip())
    if row is None:
        raise HTTPException(status_code=404, detail="session not found")
    return _row_to_summary(row)


@app.delete("/sessions/{session_id}", status_code=204)
async def delete_session(
    request: Request,
    session_id: UUID,
    x_proxy_secret: str | None = Header(default=None),
):
    settings: Settings = app.state.settings
    _check_proxy_secret(settings, x_proxy_secret)
    repo: SessionRepo = app.state.session_repo
    deleted = await repo.soft_delete(session_id)
    if not deleted:
        raise HTTPException(status_code=404, detail="session not found")
    return None


def _row_to_summary(row) -> SessionSummary:
    return SessionSummary(
        id=row.id,
        title=row.title,
        created_at=row.created_at,
        updated_at=row.updated_at,
        last_turn_at=row.last_turn_at,
        turn_count=row.turn_count,
    )


def _snapshot_to_user_facing(raw_messages: list[BaseMessage]) -> list[ChatMessage]:
    out: list[ChatMessage] = []
    for m in raw_messages:
        if isinstance(m, HumanMessage):
            text = _extract_text(m.content)
            if text:
                out.append(ChatMessage(role="user", content=text))
        elif isinstance(m, AIMessage):
            text = _extract_text(m.content)
            if text:
                # Strip the fenced JSON classifier block before showing to the user.
                visible, _ = _parse_json_block(text)
                out.append(ChatMessage(role="assistant", content=visible or text))
    return out


def extract_quant_answer(result: dict[str, Any]) -> tuple[QuantAnswer, ParseSource]:
    structured = result.get("structured_response")
    if isinstance(structured, QuantAnswer):
        return _sanitize_quant(structured), "structured"
    if isinstance(structured, dict):
        try:
            return _sanitize_quant(QuantAnswer.model_validate(structured)), "structured"
        except Exception:
            pass
    raw, _ = parse_agent_result(result)
    visible, parsed = _parse_json_block(raw)
    if parsed is not None:
        if not parsed.answer:
            parsed = parsed.model_copy(update={"answer": visible})
        return _sanitize_quant(parsed), "json_block"
    return QuantAnswer(kind="factual", answer=raw, evidence=[]), "plain"


def _sanitize_quant(q: QuantAnswer) -> QuantAnswer:
    # A recommendation with no evidence is either the model dropping citations
    # or a user prompt-injecting the kind; downgrade to analysis so the badge
    # never overstates confidence.
    if q.kind == "recommendation" and not q.evidence:
        return q.model_copy(update={"kind": "analysis"})
    return q


def _parse_json_block(text: str) -> tuple[str, QuantAnswer | None]:
    if not text:
        return text, None
    match = None
    for m in _JSON_BLOCK_RE.finditer(text):
        match = m
    if match is None:
        return text, None
    # Only accept a JSON block at the tail of the answer — a block embedded
    # mid-answer (e.g. the model quoting a JSON-shaped SQL result) is not
    # the classifier block.
    if text[match.end():].strip():
        return text, None
    try:
        payload = json.loads(match.group(1))
        quant = QuantAnswer.model_validate(payload)
    except Exception:
        return text, None
    visible = text[: match.start()].strip()
    return visible, quant


def parse_agent_result(result: dict[str, Any]) -> tuple[str, list[str]]:
    sql_queries: list[str] = []
    answer = ""
    for msg in result.get("messages", []):
        if not isinstance(msg, AIMessage):
            continue
        for call in msg.tool_calls or []:
            if call.get("name") == TOOL_SQL_DB_QUERY:
                query = (call.get("args") or {}).get("query")
                if query:
                    sql_queries.append(str(query))
        text = _extract_text(msg.content)
        if text:
            answer = text
    return answer, sql_queries


def _extract_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = [p.get("text", "") for p in content if isinstance(p, dict) and p.get("type") == "text"]
        return "\n".join(p for p in parts if p)
    return ""
