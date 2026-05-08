"""Thin shim — delegates to discord_common.scraper.

See services/discord_common/scraper.py for the implementation. The only
per-service difference is the logger name.
"""

from __future__ import annotations

import asyncio

from discord_common.scraper import cli_main


if __name__ == "__main__":
    asyncio.run(cli_main(log_name="tradingthetrend-scrape"))
