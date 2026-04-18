from pydantic_settings import BaseSettings, SettingsConfigDict

ALLOWED_TABLES = (
    "trades",
    "orders",
    "daily_pnl",
    "equity_curve",
    "strategy_daily_pnl",
    "strategy_equity_points",
    "strategy_signal_events",
    "strategy_trade_stats",
    "backtest_runs",
    "backtest_run_trades",
    "recap_digests",
    "thought_logs",
)


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_prefix="AGENT_", extra="ignore")

    db_url: str
    anthropic_api_key: str
    model: str = "claude-sonnet-4-6"
    max_output_tokens: int = 4096
    proxy_shared_secret: str = ""
    recursion_limit: int = 25
    rate_limit: str = "20/minute"
    context_ttl_seconds: float = 300.0
    context_error_ttl_seconds: float = 30.0
    omo_core_url: str = "http://localhost:8080"

    allowed_tables: tuple[str, ...] = ALLOWED_TABLES


def get_settings() -> Settings:
    return Settings()  # type: ignore[call-arg]
