import json
import logging
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, Header, HTTPException
from langchain_core.messages import AIMessage

from .agent import build_agent, to_langchain_messages
from .config import get_settings
from .schema import ChatRequest, ChatResponse

log = logging.getLogger("agent_api")
logging.basicConfig(level=logging.INFO, format="%(message)s")


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = get_settings()
    app.state.settings = settings
    app.state.agent = build_agent(settings)
    log.info(json.dumps({"event": "startup", "model": settings.model, "tables": list(settings.allowed_tables)}))
    yield


app = FastAPI(title="omo agent-api", lifespan=lifespan)


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/chat", response_model=ChatResponse)
def chat(req: ChatRequest, x_proxy_secret: str | None = Header(default=None)):
    settings = app.state.settings
    if settings.proxy_shared_secret and x_proxy_secret != settings.proxy_shared_secret:
        raise HTTPException(status_code=401, detail="invalid proxy secret")

    agent = app.state.agent
    started = time.monotonic()

    result = agent.graph.invoke(
        {"messages": to_langchain_messages(req.messages)},
        config={"recursion_limit": 25},
    )

    sql_queries: list[str] = []
    answer = ""
    for msg in result["messages"]:
        if isinstance(msg, AIMessage):
            for call in getattr(msg, "tool_calls", None) or []:
                if call.get("name") == "sql_db_query":
                    query = (call.get("args") or {}).get("query")
                    if query:
                        sql_queries.append(str(query))
            if isinstance(msg.content, str):
                answer = msg.content or answer
            elif isinstance(msg.content, list):
                parts = [p.get("text", "") for p in msg.content if isinstance(p, dict) and p.get("type") == "text"]
                joined = "\n".join(p for p in parts if p)
                answer = joined or answer

    duration_ms = int((time.monotonic() - started) * 1000)
    log.info(json.dumps({
        "event": "chat",
        "duration_ms": duration_ms,
        "turn_count": len(req.messages),
        "sql_count": len(sql_queries),
        "answer_len": len(answer),
    }))
    return ChatResponse(answer=answer, sql_queries=sql_queries, duration_ms=duration_ms)
