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
