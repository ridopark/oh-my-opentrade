from datetime import datetime
from typing import Literal
from uuid import UUID

from pydantic import BaseModel, Field

AnswerKind = Literal["factual", "analysis", "recommendation"]


class ChatMessage(BaseModel):
    role: Literal["user", "assistant"]
    content: str


class ChatRequest(BaseModel):
    session_id: UUID | None = None
    user_message: str = Field(
        ...,
        min_length=1,
        max_length=8000,
        description="Single user turn; 8000 char cap matches the LLM context budget for one Q.",
    )


class QuantAnswer(BaseModel):
    """Structured final output produced by the quant agent."""

    kind: AnswerKind = Field(
        description="factual = pure lookup; analysis = interprets metrics; recommendation = proposes a change"
    )
    answer: str = Field(description="Human-facing response; markdown allowed")
    evidence: list[str] = Field(
        default_factory=list,
        description="SQL queries or derived numbers that support an analysis or recommendation",
    )


class ChatResponse(BaseModel):
    session_id: UUID
    answer: str
    kind: AnswerKind = "factual"
    evidence: list[str] = Field(default_factory=list)
    sql_queries: list[str] = Field(default_factory=list)
    prompt_version: str = ""
    duration_ms: int = 0
    created_session: bool = False


class SessionSummary(BaseModel):
    id: UUID
    title: str
    created_at: datetime
    updated_at: datetime
    last_turn_at: datetime | None = None
    turn_count: int = 0


class SessionDetail(SessionSummary):
    messages: list[ChatMessage] = Field(default_factory=list)


class RenameRequest(BaseModel):
    title: str = Field(..., min_length=1, max_length=200)
