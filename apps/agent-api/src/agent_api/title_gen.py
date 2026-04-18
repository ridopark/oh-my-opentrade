from __future__ import annotations

import json
import logging
from uuid import UUID

from langchain_anthropic import ChatAnthropic
from langchain_core.messages import HumanMessage, SystemMessage

from .sessions import SessionRepo

log = logging.getLogger("agent_api")

_TITLE_SYSTEM = (
    "Produce a 3-6 word title summarizing the exchange. Title only, no quotes, "
    "no trailing punctuation. Lowercase unless a proper noun. Examples: "
    "'macd vs avwap last month', 'today realized pnl', 'outlier check on avwap'."
)
_TITLE_MAX_CHARS = 80


async def generate_and_persist_title(
    session_id: UUID,
    user_message: str,
    assistant_answer: str,
    anthropic_api_key: str,
    model: str,
    repo: SessionRepo,
) -> None:
    try:
        llm = ChatAnthropic(model=model, api_key=anthropic_api_key, max_tokens=64, timeout=20)
        exchange = f"User: {user_message}\n\nAssistant: {assistant_answer}"[:2000]
        resp = await llm.ainvoke([SystemMessage(content=_TITLE_SYSTEM), HumanMessage(content=exchange)])
        title = _clean_title(resp.content if isinstance(resp.content, str) else str(resp.content))
    except Exception as e:
        log.warning(json.dumps({"event": "title_gen_failed", "session_id": str(session_id), "error": str(e)}))
        return

    if not title:
        log.info(json.dumps({"event": "title_gen_empty", "session_id": str(session_id)}))
        return

    try:
        await repo.rename(session_id, title)
    except Exception as e:
        log.warning(json.dumps({
            "event": "title_gen_persist_failed",
            "session_id": str(session_id),
            "title": title,
            "error": str(e),
        }))
        return

    log.info(json.dumps({"event": "title_generated", "session_id": str(session_id), "title": title}))


def _clean_title(raw: str) -> str:
    t = raw.strip().strip('"').strip("'").splitlines()[0].strip()
    if t.endswith("."):
        t = t[:-1]
    return t[:_TITLE_MAX_CHARS]
