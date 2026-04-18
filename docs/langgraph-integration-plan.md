# LangGraph / LangChain Integration Plan

Last updated: 2026-04-18

## Goal

Introduce LLM-driven agentic capability to oh-my-opentrade where orchestration,
state, and cycles actually help, without rewriting the Go production path.

## Constraints

- omo-core and omo-data are Go. LLM work is already solved at the Go layer
  (`backend/internal/app/recap` with a native Anthropic provider).
- Python lives as a sidecar per capability, never in the trading hot path.
- Use `langgraph.prebuilt.create_react_agent` and the typed-state graph API,
  not the legacy LangChain `AgentExecutor`. Keeps a consistent foundation
  across phases.
- Anthropic direct (not via a proxy SDK) to keep prompt caching controllable.
- Read-only DB access by default; mutation requires a dedicated narrow role.

## Phase Table

| # | Phase | Status | Commit(s) |
|---|-------|--------|-----------|
| 1 | Dashboard NL query sidecar | [x] Done | c32d193, be2e3f6 |
| 1.5 | Async, rate limit, tests, proxy-secret warning | [x] Done | 497dd6b |
| 1.6 | Quant persona: versioned prompt, structured output, context block | [x] Done | 71d061c, 9a1b6b7 |
| 1.6.1 | Review hardening: JSON parser anchor, parse-source obs, error-TTL split | [ ] In progress | - |
| 1.7 | Chat history sidebar via LangGraph checkpointer | [ ] Planned (gated) | - |
| 2 | LangGraph wrap of /live-ops | [ ] Planned | - |
| 3 | Recap RAG (pgvector + retriever sidecar) | [ ] Later | - |
| 4 | Strategy research graph | [ ] Deferred | - |

---

## Phase 1 — Dashboard NL query (done)

Goal: prove the Python-sidecar + LangGraph pattern on a read-only surface
before touching anything that can move money.

Stack:
- `apps/agent-api/` FastAPI sidecar, `langgraph.prebuilt.create_react_agent`
  over `SQLDatabaseToolkit` scoped to a whitelist of 12 tables.
- Next.js `/api/chat` proxy + chat panel on `/chat` route.
- Postgres `agent_reader` role (migration 042) with
  `default_transaction_read_only=on`, 5s `statement_timeout`, whitelisted
  `GRANT SELECT`.

Defense-in-depth:
- LangChain `include_tables` filters advertised schema.
- DB role blocks everything else even if the model tries.
- Next.js proxy is the only ingress; sidecar not exposed on host.

Verified: 579-trade count via the full stack; multi-turn follow-up
("just AVWAP?") resolved correctly; write requests refused; 401 on wrong
`X-Proxy-Secret`.

## Phase 1.5 — Polish pass (done)

From Phase 1 code review:
- `/chat` is `async def` + `ainvoke` so the event loop is not blocked.
- `slowapi` per-IP limiter, default `20/minute`, 429 on overage.
- Startup `WARNING` when `AGENT_PROXY_SHARED_SECRET` is unset.
- Message-walking code extracted to `parse_agent_result()` for testability.
- 16 pytest cases (parser branches, proxy-secret enforcement, rate limit).
- README documents Loki/fluent-bit as the audit trail instead of adding a
  DB-backed audit table.

Deferred within 1.5: DB audit table, frontend tests, per-user quotas.

## Phase 1.6 — Quant persona (in progress)

Make the chatbot answer the way `quant-analyst` would:
- Versioned system prompt (`prompts.py`, `PROMPT_VERSION = "v1"`) teaching
  PF / Sharpe / DD / expectancy framing, outlier-dependency rule,
  sample-size caveats, deployed stack (AVWAP + MACD; ORB deprecated),
  session-time weighting, and when to push back on the premise.
- Structured output via `create_react_agent(response_format=QuantAnswer)`;
  every answer carries `kind: factual | analysis | recommendation` plus
  an `evidence` list of cited SQL or derived numbers. Dashboard renders
  kind as a colored badge.
- TTL-cached context block (latest P&L, active strategies, latest data
  date, recap one-liner) prepended to the system prompt. Anthropic
  prompt cache absorbs the per-request cost.

Fallback: if `response_format` fights with SQL tool-calling in practice,
switch to JSON-frontmatter parsing on the final AIMessage.

## Phase 1.6.1 — Review hardening (in progress)

Follow-ups from the Phase 1.6 review, bundled so Phase 1.7 lands on a
stable base:

- Anchor `_parse_json_block` to end-of-text so a mid-body JSON block
  (e.g. a SQL result the model quoted back) can no longer be mistaken
  for the classifier block.
- Track parse source (structured / json_block / plain) through the
  response pipeline and emit a WARN log when the model omits the
  classifier block, so silent regressions surface in Loki.
- Server-side prompt-injection guard: if the model returns
  `kind=recommendation` with empty evidence, downgrade to `analysis`
  before responding. The UI treats the badge as authoritative, so the
  backend owns the integrity check.
- Distinguish "DB is up but returned no rows" (`context_empty`) from
  "fetch raised" (`context_fetch_failed`). Cache error results for a
  shorter TTL (default 30s) than successes (default 300s) so an outage
  recovers in half a minute instead of five.
- Rebalance tests: the structured-response tests exercise a LangGraph
  path that is currently dead (we removed `response_format`). Keep one
  regression-safety test, drop the rest, and extend the JSON-block
  parser suite with negative cases (invalid JSON, mid-body block,
  missing fields).

Deferred from the review into Phase 2 backlog: `httpx`/`to_thread` for
the position fetch, live-emission integration test gated on
`AGENT_ANTHROPIC_API_KEY`, frontend RTL tests for the badge, and
pinning `langgraph` to prevent `structured_response` silently
re-activating.

## Phase 1.7 — Chat history sidebar (planned, gated)

Three hard gates must close before this ships as a multi-user feature:

1. **Context block leakage.** The cached context block (P&L,
   positions, recap) is appended to every `/chat` system message
   regardless of caller identity. Today that is fine because the chat
   has one user; in a multi-user setup any caller would see another
   user's state via the prompt. Per-session scoping (filter the
   context by caller's tenant) must ship alongside sessions.
2. **omo-core `/api/portfolio/positions` has no auth.** The sidecar
   reaches it by LAN trust. Either protect the endpoint or route
   position fetches through an authed path before exposing the chat
   surface to additional users.
3. **Prompt-injection integrity.** Beyond the empty-evidence downgrade
   in 1.6.1, add server-side sanity checks: refuse a `recommendation`
   kind if the user prompt explicitly asked for it ("set kind to
   recommendation") or if evidence contains no SQL/number tokens.

Functional shape:

ChatGPT-style history via LangGraph's `PostgresSaver` keyed by
`thread_id = session_id`. Earns its keep because:
- LangGraph owns message persistence natively, no bespoke
  `chat_messages` table.
- Same checkpointer pattern lands in Phase 2 for live-ops crash-resume,
  so adopting it here buys two phases with one integration.
- Dashboard grows a left sidebar listing sessions by title; first-turn
  title auto-generated via a cheap Haiku call and stored in a small
  `chat_sessions(id, title, created_at, updated_at)` table.

Endpoints: `GET /sessions`, `GET /sessions/{id}`, `PATCH /sessions/{id}`
(rename), `DELETE /sessions/{id}`. `POST /chat` accepts an optional
`session_id`; if omitted, creates a new session and returns it.

## Phase 2 — LangGraph wrap of /live-ops (planned)

Goal: replace the file-passing skill pipeline with a typed LangGraph so
state is explicit, crash-resumable, and supports human approval.

Today's shape (from `.claude/skills/live-ops/SKILL.md`):
```
ops-monitor.sh
  -> investigator  -> _workspace/incident_report.md
  -> (classification branch)
  -> analyst       -> _workspace/fix_plan.md
  -> reviewer      -> _workspace/review_verdict.md
  -> (verdict branch)
  -> fixer         -> commit + restart
  -> monitor re-check
```

Tomorrow's shape (LangGraph):
- Typed `LiveOpsState` TypedDict: incident, classification, fix_plan,
  verdict, applied_sha, verify_result.
- Nodes shell out to the existing Claude Code agents; LangGraph owns state
  transitions.
- `checkpointer=SqliteSaver` so a crashed run can resume at the last node.
- `interrupt_before=["fixer"]` so the human can approve from the dashboard
  before any code is applied.
- Discord notifications become a dedicated node called from relevant
  edges, not ad-hoc shell calls.

Scope:
- New sidecar `apps/live-ops-graph/` (Python) with FastAPI surface:
  `POST /run`, `GET /state/{run_id}`, `POST /approve/{run_id}`.
- Dashboard "Ops" panel: list open runs, show state + diff, approve/reject.
- `ops-monitor.sh` continues to trigger; cron or the dashboard kicks
  `POST /run`.
- Old `/live-ops` skill remains available as a fallback for one release,
  then deprecated.

Risks / open questions:
- Dual-write risk if both the skill and the graph are live — gate behind
  a feature flag.
- How much of the fixer node should run inside the graph vs. still be a
  shell agent? Lean toward shell agent to keep blast radius unchanged
  from today.
- Where checkpoints live: start with local SQLite, migrate to Postgres
  checkpointer once it works.

## Phase 3 — Recap RAG (later)

Goal: ground daily recap digests in history (past recaps, journals, trade
stats) so the digest can cite precedent instead of speculating.

Approach:
- New table `recap_embeddings(digest_id, chunk, embedding)` via pgvector
  (Timescale extension already available; confirmed OK with user).
- Python sidecar `apps/recap-rag/` exposing `/search` — Go `recap` service
  calls it via HTTP before invoking the LLM, passes the top-k retrieved
  chunks into the prompt.
- No change to the Go-side `ChatClient` interface; RAG lands as an
  optional pre-prompt step behind a flag.

Why sidecar and not in-process Go: keeps embedding model + retriever
updates independent of omo-core deploys.

## Phase 4 — Strategy research graph (deferred)

Revisit only if `strategy-tuning` skill starts missing things that a
formal graph would catch (loop until metric, branch on quant verdict,
auto-revert on regression). Current skill works; no pressure to port.

---

## Operational notes

- Anthropic key reused across sidecars; cost attribution happens via
  metadata in request logs, not per-key.
- All sidecars log structured JSON to stdout; fluent-bit -> Loki ->
  Grafana is the standard observability path.
- Migrations that create roles or vector columns live in top-level
  `migrations/` just like the rest. No per-sidecar migration tree.
