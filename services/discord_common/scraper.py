"""Discord channel history scraper — content-agnostic.

Scrolls a target channel upward, deduping by message id, until either the
oldest visible message is older than --days or the scroller stops making
progress (channel start). Writes raw {id, author, ts, text} JSONL sorted
oldest-first; downstream parsers consume this on their own grammar.

Each sidecar wraps `cli_main(log_name=...)` in a thin __main__ shim. The
JS extraction blocks are pinned alongside `discord_dom.py` so any future
DOM-rewrite fix lands in one place.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import pathlib
import sys
from datetime import datetime, timedelta, timezone

from dotenv import load_dotenv
from playwright.async_api import async_playwright


DISCORD_EPOCH_MS = 1420070400000


def _setup_logging(log_name: str, level: str) -> logging.Logger:
    lvl = getattr(logging, level.upper(), logging.INFO)
    logging.basicConfig(
        level=lvl,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
        stream=sys.stdout,
    )
    return logging.getLogger(log_name)


def snowflake_to_dt(message_row_id: str) -> datetime | None:
    parts = message_row_id.rsplit("-", 1)
    if len(parts) != 2 or not parts[1].isdigit():
        return None
    ts_ms = (int(parts[1]) >> 22) + DISCORD_EPOCH_MS
    return datetime.fromtimestamp(ts_ms / 1000, tz=timezone.utc)


_EXTRACT_ALL_JS = r"""
(() => {
  const out = [];
  const items = document.querySelectorAll('li[id^="chat-messages-"]');
  for (const li of items) {
    const id = li.getAttribute('id') || '';
    let author = '';
    let headerLi = li;
    while (headerLi) {
      const h = headerLi.querySelector('h3 span[class*="username"]');
      if (h) { author = (h.textContent || '').trim(); break; }
      headerLi = headerLi.previousElementSibling;
      if (!headerLi || !headerLi.matches('li[id^="chat-messages-"]')) break;
    }
    const tEl = li.querySelector('time[datetime]');
    const ts = tEl ? tEl.getAttribute('datetime') : '';
    const contentEl = li.querySelector('div[id^="message-content-"]');
    let text = '';
    if (contentEl) {
      text = contentEl.innerText || contentEl.textContent || '';
    }
    out.push({ id, author, ts, text });
  }
  return out;
})()
"""

_SCROLL_UP_JS = r"""
(() => {
  const candidates = document.querySelectorAll('[class*="scroller"]');
  let scroller = null;
  for (const el of candidates) {
    if (el.querySelector('li[id^="chat-messages-"]')) { scroller = el; break; }
  }
  if (!scroller) return -1;
  const before = scroller.scrollTop;
  scroller.scrollTop = Math.max(0, before - scroller.clientHeight * 3);
  return scroller.scrollTop;
})()
"""


async def extract_all(page) -> list[dict]:
    return await page.evaluate(_EXTRACT_ALL_JS)


async def scroll_up(page) -> int:
    return await page.evaluate(_SCROLL_UP_JS)


async def run(
    channel_url: str,
    storage_state_path: pathlib.Path,
    out_path: pathlib.Path,
    days: int,
    max_stale_ticks: int,
    scroll_settle_secs: float,
    log: logging.Logger,
) -> None:
    if not storage_state_path.exists():
        log.error("storage_state.json missing at %s — run bootstrap first", storage_state_path)
        sys.exit(2)

    cutoff = datetime.now(tz=timezone.utc) - timedelta(days=days)
    log.info("target cutoff: %s (%d days)", cutoff.isoformat(), days)

    collected: dict[str, dict] = {}
    stale_ticks = 0
    prev_oldest_id: str | None = None
    tick = 0

    async with async_playwright() as pw:
        browser = await pw.chromium.launch(headless=True)
        context = await browser.new_context(storage_state=str(storage_state_path))
        page = await context.new_page()
        log.info("navigating to %s", channel_url)
        await page.goto(channel_url, wait_until="domcontentloaded")
        await page.wait_for_selector('li[id^="chat-messages-"]', timeout=30_000)

        # Click once inside the chat area to give the scroller focus. Without
        # this the scroller can ignore programmatic scrollTop assignments on
        # some Discord builds.
        try:
            await page.click('li[id^="chat-messages-"]', timeout=5_000)
        except Exception:
            pass

        while True:
            tick += 1
            rows = await extract_all(page)
            new = 0
            for r in rows:
                mid = r.get("id") or ""
                if not mid or mid in collected:
                    continue
                collected[mid] = r
                new += 1

            oldest_dom_id = min((r["id"] for r in rows if r.get("id")), default=None)
            if oldest_dom_id is None:
                log.warning("tick=%d no messages in DOM — aborting", tick)
                break
            oldest_dom_ts = snowflake_to_dt(oldest_dom_id)

            log.info(
                "tick=%d in_dom=%d new=%d total=%d oldest_dom=%s",
                tick, len(rows), new, len(collected),
                oldest_dom_ts.isoformat() if oldest_dom_ts else "?",
            )

            if oldest_dom_ts and oldest_dom_ts < cutoff:
                log.info("oldest in DOM is past cutoff — done")
                break

            if oldest_dom_id == prev_oldest_id:
                stale_ticks += 1
                if stale_ticks >= max_stale_ticks:
                    log.info("no scroll progress for %d ticks — assuming channel start", stale_ticks)
                    break
            else:
                stale_ticks = 0
            prev_oldest_id = oldest_dom_id

            scroll_top = await scroll_up(page)
            if scroll_top < 0:
                log.error("tick=%d could not locate scroller element — aborting", tick)
                break
            await asyncio.sleep(scroll_settle_secs)

        await browser.close()

    records: list[dict] = []
    for mid, r in collected.items():
        ts_iso = r.get("ts") or ""
        dt = None
        if ts_iso:
            try:
                dt = datetime.fromisoformat(ts_iso.replace("Z", "+00:00"))
            except ValueError:
                dt = None
        if dt is None:
            dt = snowflake_to_dt(mid)
        if dt is None or dt < cutoff:
            continue
        records.append({
            "id": mid,
            "author": r.get("author") or "",
            "ts": dt.astimezone(timezone.utc).isoformat(),
            "text": r.get("text") or "",
        })

    records.sort(key=lambda r: r["ts"])
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("w") as fh:
        for rec in records:
            fh.write(json.dumps(rec, ensure_ascii=False) + "\n")

    if records:
        log.info(
            "wrote %d records to %s (%s → %s)",
            len(records), out_path, records[0]["ts"], records[-1]["ts"],
        )
    else:
        log.warning("no records within cutoff window — %s is empty", out_path)


async def cli_main(log_name: str) -> None:
    """Entry point invoked by per-service __main__ shims.

    The only per-service variable is the logger name, so the CLI parser,
    env loading, and call into run() are shared.
    """
    load_dotenv()
    ap = argparse.ArgumentParser()
    ap.add_argument("--days", type=int, default=90, help="history window in days")
    ap.add_argument("--out", default="/app/state/history.jsonl", help="output JSONL path")
    ap.add_argument("--max-stale-ticks", type=int, default=6, help="stop after N scroll ticks with no new oldest id")
    ap.add_argument("--scroll-settle-secs", type=float, default=1.2, help="wait between scroll ticks")
    ap.add_argument("--log-level", default=os.getenv("LOG_LEVEL", "info"))
    args = ap.parse_args()

    log = _setup_logging(log_name, args.log_level)
    channel_url = os.getenv("DISCORD_CHANNEL_URL", "").strip()
    state_dir = pathlib.Path(os.getenv("STATE_DIR", "./state"))
    if not channel_url:
        log.error("DISCORD_CHANNEL_URL is required")
        sys.exit(2)

    await run(
        channel_url=channel_url,
        storage_state_path=state_dir / "storage_state.json",
        out_path=pathlib.Path(args.out),
        days=args.days,
        max_stale_ticks=args.max_stale_ticks,
        scroll_settle_secs=args.scroll_settle_secs,
        log=log,
    )
