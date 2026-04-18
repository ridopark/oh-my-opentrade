from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Optional
from uuid import UUID, uuid4

from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, async_sessionmaker, create_async_engine


@dataclass(slots=True)
class ChatSessionRow:
    id: UUID
    title: str
    created_at: datetime
    updated_at: datetime
    last_turn_at: Optional[datetime]
    turn_count: int


class SessionRepo:
    def __init__(self, engine: AsyncEngine):
        self._engine = engine
        self._session = async_sessionmaker(engine, expire_on_commit=False)

    async def create(self, title: str = "New chat") -> ChatSessionRow:
        sid = uuid4()
        now = datetime.now(UTC)
        async with self._session() as s, s.begin():
            await s.execute(
                text(
                    "INSERT INTO chat_sessions (id, title, created_at, updated_at) "
                    "VALUES (:id, :title, :now, :now)"
                ),
                {"id": str(sid), "title": title, "now": now},
            )
        return ChatSessionRow(
            id=sid, title=title, created_at=now, updated_at=now,
            last_turn_at=None, turn_count=0,
        )

    async def get(self, session_id: UUID) -> Optional[ChatSessionRow]:
        async with self._session() as s:
            row = (await s.execute(
                text(
                    "SELECT id, title, created_at, updated_at, last_turn_at, turn_count "
                    "FROM chat_sessions WHERE id = :id AND deleted_at IS NULL"
                ),
                {"id": str(session_id)},
            )).mappings().first()
        return _row(row) if row else None

    async def list(self, limit: int = 50) -> list[ChatSessionRow]:
        async with self._session() as s:
            rows = (await s.execute(
                text(
                    "SELECT id, title, created_at, updated_at, last_turn_at, turn_count "
                    "FROM chat_sessions WHERE deleted_at IS NULL "
                    "ORDER BY updated_at DESC LIMIT :limit"
                ),
                {"limit": limit},
            )).mappings().all()
        return [_row(r) for r in rows]

    async def rename(self, session_id: UUID, title: str) -> Optional[ChatSessionRow]:
        # Two-query pattern (UPDATE + follow-up SELECT) keeps this working
        # against both Postgres in prod and aiosqlite in tests — SQLite does
        # not support UPDATE ... RETURNING through aiosqlite's driver.
        now = datetime.now(UTC)
        async with self._session() as s, s.begin():
            await s.execute(
                text(
                    "UPDATE chat_sessions SET title = :title, updated_at = :now "
                    "WHERE id = :id AND deleted_at IS NULL"
                ),
                {"id": str(session_id), "title": title, "now": now},
            )
        return await self.get(session_id)

    async def soft_delete(self, session_id: UUID) -> bool:
        async with self._session() as s, s.begin():
            result = await s.execute(
                text(
                    "UPDATE chat_sessions SET deleted_at = :now "
                    "WHERE id = :id AND deleted_at IS NULL"
                ),
                {"id": str(session_id), "now": datetime.now(UTC)},
            )
        return result.rowcount > 0

    async def bump_turn(self, session_id: UUID) -> None:
        now = datetime.now(UTC)
        async with self._session() as s, s.begin():
            await s.execute(
                text(
                    "UPDATE chat_sessions "
                    "SET turn_count = turn_count + 1, last_turn_at = :now, updated_at = :now "
                    "WHERE id = :id AND deleted_at IS NULL"
                ),
                {"id": str(session_id), "now": now},
            )


def _row(r) -> ChatSessionRow:
    return ChatSessionRow(
        id=r["id"] if isinstance(r["id"], UUID) else UUID(str(r["id"])),
        title=r["title"],
        created_at=r["created_at"],
        updated_at=r["updated_at"],
        last_turn_at=r["last_turn_at"],
        turn_count=r["turn_count"],
    )


def make_async_engine(db_url: str) -> AsyncEngine:
    # SQLAlchemy async needs the psycopg (v3) driver; the env may pass the
    # sync psycopg2 URL, so normalize.
    url = db_url.replace("+psycopg2", "+psycopg")
    if url.startswith("postgresql://"):
        url = "postgresql+psycopg://" + url[len("postgresql://"):]
    return create_async_engine(url, pool_pre_ping=True, pool_size=3, max_overflow=2)
