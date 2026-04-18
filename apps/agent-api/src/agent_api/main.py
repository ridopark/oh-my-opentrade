import json
import logging
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
from .schema import ChatRequest, ChatResponse

TOOL_SQL_DB_QUERY = "sql_db_query"

log = logging.getLogger("agent_api")
logging.basicConfig(level=logging.INFO, format="%(message)s")

limiter = Limiter(key_func=get_remote_address)


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = get_settings()
    app.state.settings = settings
    app.state.agent = build_agent(settings)
    log.info(json.dumps({"event": "startup", "model": settings.model, "tables": list(settings.allowed_tables)}))
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

    answer, sql_queries = parse_agent_result(result)

    duration_ms = int((time.monotonic() - started) * 1000)
    log.info(json.dumps({
        "event": "chat",
        "duration_ms": duration_ms,
        "turn_count": len(req.messages),
        "sql_count": len(sql_queries),
        "answer_len": len(answer),
    }))
    return ChatResponse(answer=answer, sql_queries=sql_queries, duration_ms=duration_ms)


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
