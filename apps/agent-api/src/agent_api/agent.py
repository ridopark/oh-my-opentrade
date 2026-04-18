from dataclasses import dataclass
from langchain_anthropic import ChatAnthropic
from langchain_community.agent_toolkits.sql.toolkit import SQLDatabaseToolkit
from langchain_community.utilities import SQLDatabase
from langchain_core.messages import AIMessage, HumanMessage
from langgraph.prebuilt import create_react_agent

from .config import Settings
from .schema import ChatMessage

SYSTEM_PROMPT = """You are a read-only SQL analyst for the oh-my-opentrade trading system.

Answer the user's question by querying the Postgres database through the provided tools.

Rules:
- Only query tables listed in your toolkit schema. The database role is read-only; any attempted write will fail.
- Prefer a single aggregate query over many row-level queries.
- Always add LIMIT 500 to queries that could return large row sets.
- Time columns are TIMESTAMPTZ. The app's trading day is US/Eastern.
- Dollar amounts in `daily_pnl`, `strategy_daily_pnl`, and `trades` are already net of fees.
- If the question is ambiguous, ask a brief clarifying question instead of guessing.
- If the question is outside the scope of the whitelisted tables, say so plainly.

Respond with a concise answer. When numbers matter, include them inline. Do not apologize."""


@dataclass
class AgentBundle:
    graph: object
    db: SQLDatabase


def build_agent(settings: Settings) -> AgentBundle:
    db = SQLDatabase.from_uri(
        settings.db_url,
        include_tables=list(settings.allowed_tables),
        sample_rows_in_table_info=2,
    )
    llm = ChatAnthropic(
        model=settings.model,
        api_key=settings.anthropic_api_key,
        max_tokens=settings.max_output_tokens,
        timeout=60,
    )
    toolkit = SQLDatabaseToolkit(db=db, llm=llm)
    graph = create_react_agent(llm, toolkit.get_tools(), prompt=SYSTEM_PROMPT)
    return AgentBundle(graph=graph, db=db)


def to_langchain_messages(messages: list[ChatMessage]):
    converted = []
    for m in messages:
        if m.role == "user":
            converted.append(HumanMessage(content=m.content))
        else:
            converted.append(AIMessage(content=m.content))
    return converted
