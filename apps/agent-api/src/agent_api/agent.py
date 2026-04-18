from dataclasses import dataclass
from typing import Callable

from langchain_anthropic import ChatAnthropic
from langchain_community.agent_toolkits.sql.toolkit import SQLDatabaseToolkit
from langchain_community.utilities import SQLDatabase
from langchain_core.messages import AIMessage, HumanMessage, SystemMessage
from langgraph.prebuilt import create_react_agent

from .config import Settings
from .context import ContextBuilder
from .prompts import QUANT_V1_SYSTEM_PROMPT
from .schema import ChatMessage
from .tools import make_performance_tools


@dataclass
class AgentBundle:
    graph: object
    db: SQLDatabase


def build_agent(settings: Settings, context_builder: ContextBuilder | None = None) -> AgentBundle:
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
    tools = list(toolkit.get_tools())
    if settings.omo_core_url:
        tools.extend(make_performance_tools(settings.omo_core_url))
    prompt_fn = _make_prompt_fn(context_builder)
    graph = create_react_agent(
        llm,
        tools,
        prompt=prompt_fn,
    )
    return AgentBundle(graph=graph, db=db)


def _make_prompt_fn(context_builder: ContextBuilder | None) -> Callable:
    def prompt_fn(state):
        parts = [QUANT_V1_SYSTEM_PROMPT]
        if context_builder is not None:
            ctx = context_builder.build()
            if ctx:
                parts.append(ctx)
        system = SystemMessage(content="\n\n".join(parts))
        return [system] + list(state["messages"])

    return prompt_fn


def to_langchain_messages(messages: list[ChatMessage]):
    converted = []
    for m in messages:
        if m.role == "user":
            converted.append(HumanMessage(content=m.content))
        else:
            converted.append(AIMessage(content=m.content))
    return converted
