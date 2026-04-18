from typing import Literal
from pydantic import BaseModel, Field

AnswerKind = Literal["factual", "analysis", "recommendation"]


class ChatMessage(BaseModel):
    role: Literal["user", "assistant"]
    content: str


class ChatRequest(BaseModel):
    messages: list[ChatMessage] = Field(..., min_length=1, max_length=40)


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
    answer: str
    kind: AnswerKind = "factual"
    evidence: list[str] = Field(default_factory=list)
    sql_queries: list[str] = Field(default_factory=list)
    prompt_version: str = ""
    duration_ms: int = 0
