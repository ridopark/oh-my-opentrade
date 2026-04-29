"""Discord channel watcher.

Polls the channel DOM every POLL_INTERVAL_SECS, tracks message IDs it has
already processed, parses trade lines, and POSTs each parsed signal to
omo-core. Heartbeats to /app/state/heartbeat so the container healthcheck
can detect a wedged watcher.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import pathlib
import sys
from datetime import datetime, timezone

from dotenv import load_dotenv
from playwright.async_api import async_playwright

from discord_dom import extract_recent
from emit import Emitter, SignalPayload, iso_date
from parser import parse_message


def _setup_logging(level: str) -> logging.Logger:
    lvl = getattr(logging, level.upper(), logging.INFO)
    logging.basicConfig(
        level=lvl,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
        stream=sys.stdout,
    )
    return logging.getLogger("copytrade")


class Watcher:
    def __init__(
        self,
        channel_url: str,
        state_dir: pathlib.Path,
        emitter: Emitter,
        log: logging.Logger,
        poll_interval_secs: float,
    ) -> None:
        self._channel_url = channel_url
        self._state_dir = state_dir
        self._emitter = emitter
        self._log = log
        self._poll_interval = poll_interval_secs
        self._seen_path = state_dir / "seen_ids.json"
        self._heartbeat_path = state_dir / "heartbeat"
        self._storage_state_path = state_dir / "storage_state.json"
        self._seen: set[str] = set()

    def _load_seen(self) -> None:
        if self._seen_path.exists():
            try:
                self._seen = set(json.loads(self._seen_path.read_text()))
            except Exception as e:
                self._log.warning("failed to load seen_ids.json: %s", e)
                self._seen = set()

    def _persist_seen(self) -> None:
        # Keep the file bounded; last ~500 is more than enough.
        bounded = list(self._seen)[-500:]
        self._seen_path.write_text(json.dumps(bounded))

    def _heartbeat(self) -> None:
        self._heartbeat_path.touch()

    async def run(self) -> None:
        if not self._storage_state_path.exists():
            self._log.error(
                "storage_state.json missing at %s — run bootstrap first (see README)",
                self._storage_state_path,
            )
            sys.exit(2)

        self._load_seen()
        async with async_playwright() as pw:
            browser = await pw.chromium.launch(headless=True)
            context = await browser.new_context(storage_state=str(self._storage_state_path))
            page = await context.new_page()
            self._log.info("navigating to %s", self._channel_url)
            await page.goto(self._channel_url, wait_until="domcontentloaded")
            # Give the chat a moment to render before the first scrape.
            await page.wait_for_selector('li[id^="chat-messages-"]', timeout=30_000)

            # First pass: seed the seen set with whatever is already on screen.
            # Without this, we'd fire signals for every historical message on
            # startup, which is both wrong and dangerous.
            if not self._seen:
                initial = await extract_recent(page, limit=50)
                self._seen = {m.message_id for m in initial}
                self._persist_seen()
                self._log.info("seeded %d existing messages as seen", len(self._seen))

            while True:
                try:
                    await self._tick(page)
                except Exception as e:
                    self._log.exception("tick error: %s", e)
                self._heartbeat()
                await asyncio.sleep(self._poll_interval)

    async def _tick(self, page) -> None:
        msgs = await extract_recent(page, limit=25)
        for m in msgs:
            if m.message_id in self._seen:
                continue
            self._seen.add(m.message_id)
            if not m.content.strip():
                continue
            parsed = parse_message(m.content)
            if not parsed:
                self._log.debug("no signal in message %s from %s", m.message_id, m.author)
                continue
            for i, sig in enumerate(parsed):
                payload = SignalPayload(
                    signal_id=f"{m.message_id}:{i}",
                    message_id=m.message_id,
                    author=m.author,
                    posted_at=m.timestamp_iso or datetime.now(timezone.utc).isoformat(),
                    action=sig.action,
                    ticker=sig.ticker,
                    expiry=iso_date(sig.expiry),
                    strike=sig.strike,
                    right=sig.right,
                    price=sig.price,
                    tail=sig.tail,
                    raw_line=sig.raw_line,
                )
                status, body = await self._emitter.emit(payload)
                if status >= 300:
                    self._log.warning(
                        "emit non-2xx: %d body=%s payload=%s", status, body, payload
                    )
                else:
                    self._log.info(
                        "emitted %s %s %s %s%s @ %s (author=%s)",
                        payload.action,
                        payload.ticker,
                        payload.expiry,
                        payload.strike,
                        payload.right,
                        payload.price,
                        payload.author,
                    )
        self._persist_seen()


async def main() -> None:
    load_dotenv()
    log = _setup_logging(os.getenv("LOG_LEVEL", "info"))

    channel_url = os.getenv("DISCORD_CHANNEL_URL", "").strip()
    backend_url = os.getenv("OMO_BACKEND_URL", "http://localhost:8080").strip()
    secret = os.getenv("OMO_COPYTRADE_SECRET", "").strip()
    state_dir = pathlib.Path(os.getenv("STATE_DIR", "./state"))
    poll_interval = float(os.getenv("POLL_INTERVAL_SECS", "1.0"))

    if not channel_url:
        log.error("DISCORD_CHANNEL_URL is required")
        sys.exit(2)
    if not secret:
        log.error("OMO_COPYTRADE_SECRET is required")
        sys.exit(2)

    state_dir.mkdir(parents=True, exist_ok=True)

    emitter = Emitter(backend_url=backend_url, secret=secret)
    watcher = Watcher(
        channel_url=channel_url,
        state_dir=state_dir,
        emitter=emitter,
        log=log,
        poll_interval_secs=poll_interval,
    )
    try:
        await watcher.run()
    finally:
        await emitter.close()


if __name__ == "__main__":
    asyncio.run(main())
