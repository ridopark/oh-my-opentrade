# agent-api

Natural-language query sidecar for the oh-my-opentrade dashboard. Receives a
multi-turn conversation from the Next.js `/api/chat` proxy, runs a LangChain
SQL agent over a read-only Postgres role, returns the answer plus the generated
SQL for transparency.

## Dev setup

    cd apps/agent-api
    uv sync --extra dev
    cp .env.example .env          # edit AGENT_ANTHROPIC_API_KEY + rotate password
    uv run uvicorn agent_api.main:app --reload --port 8100

## Provisioning the read-only role

Migration `042_create_agent_reader_role` creates `agent_reader` with the dev
password `changeme_agent_reader`. Before running the sidecar against any
non-local database, rotate it:

    ALTER ROLE agent_reader WITH PASSWORD '<strong-random>';

Then set `AGENT_DB_URL` to match.

## Smoke test

    curl -s http://localhost:8100/health
    curl -s http://localhost:8100/chat \
      -H 'Content-Type: application/json' \
      -d '{"messages":[{"role":"user","content":"what was total realized P&L last 7 days?"}]}' \
      | jq

## Tests

    uv run pytest

## Audit trail

Every `/chat` call emits a structured JSON log line (`event: chat`) to stdout
with turn count, SQL count, duration, and answer length. In the prod container
stack these are picked up by fluent-bit -> Loki, searchable via Grafana. No
DB-side audit table is maintained.

## Rate limit

Requests to `/chat` are throttled per remote IP. Default is `20/minute`; tune
via `AGENT_RATE_LIMIT` using any [slowapi](https://slowapi.readthedocs.io/)
string (e.g. `5/second`, `100/hour`). When exceeded the endpoint returns 429.

## Proxy secret

If `AGENT_PROXY_SHARED_SECRET` is set, requests must send matching
`X-Proxy-Secret`. When unset the sidecar logs a startup WARNING and accepts
unauthenticated requests - dev only. The Next.js `/api/chat` proxy reads the
secret from its own env and injects the header.
