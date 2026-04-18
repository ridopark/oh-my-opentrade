import json
import logging
import re
import time
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, Header, HTTPException, Request
from langchain_core.messages import AIMessage
from slowapi import Limiter, _rate_limit_exceeded_handler
from slowapi.errors import RateLimitExceeded
from slowapi.util import get_remote_address

from .agent import build_agent, to_langchain_messages
from .config import Settings, get_settings
from .context import ContextBuilder, make_fetcher
from .prompts import PROMPT_VERSION
from .schema import ChatRequest, ChatResponse, QuantAnswer

TOOL_SQL_DB_QUERY = "sql_db_query"
_JSON_BLOCK_RE = re.compile(r"```(?:json)?\s*(\{[\s\S]*?\})\s*```", re.MULTILINE)

log = logging.getLogger("agent_api")
logging.basicConfig(level=logging.INFO, format="%(message)s")

limiter = Limiter(key_func=get_remote_address)


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = get_settings()
    app.state.settings = settings
    context_builder = ContextBuilder(
        fetcher=make_fetcher(settings.db_url, settings.omo_core_url),
        ttl_seconds=settings.context_ttl_seconds,
    )
    app.state.context_builder = context_builder
    app.state.agent = build_agent(settings, context_builder=context_builder)
    log.info(json.dumps({
        "event": "startup",
        "model": settings.model,
        "prompt_version": PROMPT_VERSION,
        "tables": list(settings.allowed_tables),
    }))
    if not settings.proxy_shared_secret:
        log.warning(json.dumps({"event": "warn", "msg": "AGENT_PROXY_SHARED_SECRET is empty; /chat accepts unauthenticated requests"}))
    yield


app = FastAPI(title="omo agent-api", lifespan=lifespan)
app.state.limiter = limiter
app.add_exception_handler(RateLimitExceeded, _rate_limit_exceeded_handler)


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
@limiter.limit(lambda: app.state.settings.rate_limit)
async def chat(request: Request, req: ChatRequest, x_proxy_secret: str | None = Header(default=None)):
    settings: Settings = app.state.settings
    if settings.proxy_shared_secret and x_proxy_secret != settings.proxy_shared_secret:
        raise HTTPException(status_code=401, detail="invalid proxy secret")

    agent = app.state.agent
    started = time.monotonic()

    result = await agent.graph.ainvoke(
        {"messages": to_langchain_messages(req.messages)},
        config={"recursion_limit": settings.recursion_limit},
    )

    quant = extract_quant_answer(result)
    _, sql_queries = parse_agent_result(result)
    duration_ms = int((time.monotonic() - started) * 1000)

    log.info(json.dumps({
        "event": "chat",
        "duration_ms": duration_ms,
        "turn_count": len(req.messages),
        "sql_count": len(sql_queries),
        "kind": quant.kind,
        "evidence_count": len(quant.evidence),
        "answer_len": len(quant.answer),
        "prompt_version": PROMPT_VERSION,
    }))
    return ChatResponse(
        answer=quant.answer,
        kind=quant.kind,
        evidence=quant.evidence,
        sql_queries=sql_queries,
        prompt_version=PROMPT_VERSION,
        duration_ms=duration_ms,
    )


def extract_quant_answer(result: dict[str, Any]) -> QuantAnswer:
    structured = result.get("structured_response")
    if isinstance(structured, QuantAnswer):
        return structured
    if isinstance(structured, dict):
        try:
            return QuantAnswer.model_validate(structured)
        except Exception:
            pass
    raw, _ = parse_agent_result(result)
    visible, parsed = _parse_json_block(raw)
    if parsed is not None:
        if not parsed.answer:
            parsed = parsed.model_copy(update={"answer": visible})
        return parsed
    return QuantAnswer(kind="factual", answer=raw, evidence=[])


def _parse_json_block(text: str) -> tuple[str, QuantAnswer | None]:
    if not text:
        return text, None
    match = None
    for m in _JSON_BLOCK_RE.finditer(text):
        match = m
    if match is None:
        return text, None
    try:
        payload = json.loads(match.group(1))
        quant = QuantAnswer.model_validate(payload)
    except Exception:
        return text, None
    visible = (text[: match.start()] + text[match.end():]).strip()
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
