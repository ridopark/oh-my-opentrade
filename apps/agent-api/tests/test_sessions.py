import pytest
import pytest_asyncio
from sqlalchemy import text
from sqlalchemy.ext.asyncio import create_async_engine

from agent_api.sessions import SessionRepo


SCHEMA = """
CREATE TABLE chat_sessions (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL DEFAULT 'New chat',
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_turn_at TIMESTAMP,
    turn_count INTEGER NOT NULL DEFAULT 0,
    deleted_at TIMESTAMP
);
"""


@pytest_asyncio.fixture
async def repo():
    engine = create_async_engine("sqlite+aiosqlite:///:memory:")
    async with engine.begin() as conn:
        await conn.execute(text(SCHEMA))
    yield SessionRepo(engine)
    await engine.dispose()


@pytest.mark.asyncio
async def test_create_then_get(repo):
    row = await repo.create(title="hello")
    assert row.title == "hello"
    assert row.turn_count == 0
    fetched = await repo.get(row.id)
    assert fetched is not None
    assert fetched.id == row.id
    assert fetched.title == "hello"


@pytest.mark.asyncio
async def test_get_missing_returns_none(repo):
    from uuid import uuid4
    assert await repo.get(uuid4()) is None


@pytest.mark.asyncio
async def test_list_sorted_by_updated_at(repo):
    import asyncio
    a = await repo.create(title="a")
    await asyncio.sleep(0.005)
    b = await repo.create(title="b")
    await asyncio.sleep(0.005)
    await repo.bump_turn(a.id)

    rows = await repo.list()
    assert [r.title for r in rows[:2]] == ["a", "b"]


@pytest.mark.asyncio
async def test_rename(repo):
    row = await repo.create(title="initial")
    renamed = await repo.rename(row.id, "final")
    assert renamed is not None
    assert renamed.title == "final"
    fetched = await repo.get(row.id)
    assert fetched is not None and fetched.title == "final"


@pytest.mark.asyncio
async def test_rename_missing(repo):
    from uuid import uuid4
    assert await repo.rename(uuid4(), "x") is None


@pytest.mark.asyncio
async def test_soft_delete_hides_from_get_and_list(repo):
    row = await repo.create(title="goodbye")
    assert await repo.soft_delete(row.id) is True
    assert await repo.get(row.id) is None
    assert all(r.id != row.id for r in await repo.list())


@pytest.mark.asyncio
async def test_soft_delete_idempotent_returns_false_second_time(repo):
    row = await repo.create(title="once")
    assert await repo.soft_delete(row.id) is True
    assert await repo.soft_delete(row.id) is False


@pytest.mark.asyncio
async def test_bump_turn_increments_count_and_last_turn_at(repo):
    row = await repo.create(title="bump")
    assert row.last_turn_at is None
    await repo.bump_turn(row.id)
    fetched = await repo.get(row.id)
    assert fetched is not None
    assert fetched.turn_count == 1
    assert fetched.last_turn_at is not None


@pytest.mark.asyncio
async def test_bump_turn_on_deleted_is_noop(repo):
    row = await repo.create(title="x")
    await repo.soft_delete(row.id)
    await repo.bump_turn(row.id)  # Should not raise, should not mutate.
