# KakaoTalk Notification Channel

## TL;DR

> **Quick Summary**: Add KakaoTalk "Send to Me" (Memo API) as a third notification channel alongside existing Discord and Telegram adapters. This requires a stateful OAuth2 adapter with token persistence — a new pattern for this codebase since existing adapters are stateless.
> 
> **Deliverables**:
> - KakaoNotifier adapter implementing `NotifierPort` with auto token refresh
> - OAuth2 login flow: backend HTTP handler + frontend Settings page
> - Token persistence via TimescaleDB (new `kakao_tokens` table)
> - Kakao message template builder (text → JSON template → form-encoded body)
> - Frontend Settings page with connect/disconnect/status UI
> 
> **Estimated Effort**: Medium (13 implementation tasks + 4 verification)
> **Parallel Execution**: YES — 3 waves + 1 final verification wave
> **Critical Path**: T1 (config) → T7 (adapter) → T11 (wiring) → F1-F4 (verification)

---

## Context

### Original Request
Add KakaoTalk as a third notification channel in the oh-my-opentrade algorithmic trading platform, alongside the existing Discord and Telegram adapters. The Kakao "Send to Me" Memo API was chosen as the only viable path for personal/small-team use without Korean business registration.

### Research Findings
- **API**: `POST https://kapi.kakao.com/v2/api/talk/memo/default/send` — free, self-notification only
- **Auth**: OAuth2 with short-lived access tokens (6-12h) and refresh tokens (~2 months)
- **Content-Type**: `application/x-www-form-urlencoded` (NOT JSON like Discord/Telegram — critical difference)
- **Message format**: `template_object` parameter containing URL-encoded JSON with mandatory `link.web_url` field
- **Required scope**: `talk_message`
- **Rate limits**: 30,000/day per app, 100/day per user (tight for active trading)
- **No official Go SDK** — custom REST client needed
- **Token URLs**: Auth `https://kauth.kakao.com/oauth/authorize`, Token `https://kauth.kakao.com/oauth/token`

### Key Architectural Difference from Existing Adapters
Discord and Telegram adapters are **stateless** — they take static credentials (webhook URL or bot token) and fire HTTP requests. The Kakao adapter is **stateful** — it must persist OAuth2 tokens in the database, handle token refresh cycles, and manage a "configured but not connected" state that has no precedent in the current notification system.

### Metis Review — Identified Gaps (addressed)
1. **Single-operator vs. multi-tenant**: Resolved as single-operator for MVP (matches "Send to Me" self-notification nature). Schema includes `tenant_id` for future-proofing
2. **OAuth callback architecture**: Backend handles OAuth directly (not proxied through frontend). Frontend only initiates redirect and displays status
3. **Token encryption**: Plaintext in DB for MVP (matches existing pattern where no secrets are stored in DB). `// TODO: encrypt at rest` comment added
4. **Mandatory link field**: Configurable via `KAKAO_LINK_URL` env var with sensible default
5. **golang.org/x/oauth2**: Adopted — provides thread-safe `ReuseTokenSource` for concurrent notifications
6. **Rate limit (100/user/day)**: Simple daily counter inside adapter with log warning and graceful skip
7. **CSRF protection**: HMAC-signed state parameter (self-contained, no session store)
8. **Content-Type trap**: Kakao uses `application/x-www-form-urlencoded` — explicitly called out in task descriptions and tests
9. **"Configured but not connected" state**: Wiring checks BOTH config AND valid tokens. Status endpoint surfaces connection state

---

## Work Objectives

### Core Objective
Enable trading notifications (order fills, risk alerts, system status) to be delivered via KakaoTalk alongside existing Discord and Telegram channels, with a one-time OAuth2 setup flow.

### Concrete Deliverables
- `backend/internal/adapters/notification/kakao.go` — KakaoNotifier adapter
- `backend/internal/adapters/notification/kakao_template.go` — Message template builder
- `backend/internal/adapters/notification/kakao_test.go` — Adapter tests
- `backend/internal/adapters/notification/kakao_template_test.go` — Template tests
- `backend/internal/adapters/http/kakao_auth.go` — OAuth HTTP handler
- `backend/internal/adapters/http/kakao_auth_test.go` — OAuth handler tests
- `backend/internal/adapters/http/kakao_state.go` — HMAC state utility
- `backend/internal/adapters/http/kakao_state_test.go` — State utility tests
- `backend/internal/adapters/timescaledb/kakao_token_repo.go` — Token repository
- `backend/internal/adapters/timescaledb/kakao_token_repo_test.go` — Repo tests
- `backend/internal/ports/kakao_token.go` — Token store port interface
- `backend/internal/config/config.go` — Extended NotificationConfig
- `configs/config.yaml` — Updated with Kakao comments
- `migrations/015_create_kakao_tokens.up.sql` — Up migration
- `migrations/015_create_kakao_tokens.down.sql` — Down migration
- `backend/cmd/omo-core/main.go` — KakaoNotifier wiring + OAuth handler registration
- `apps/dashboard/app/settings/page.tsx` — Settings page
- `apps/dashboard/app/api/auth/kakao/start/route.ts` — OAuth start proxy
- `apps/dashboard/app/api/auth/kakao/status/route.ts` — Status proxy
- `apps/dashboard/app/api/auth/kakao/disconnect/route.ts` — Disconnect proxy
- `apps/dashboard/components/sidebar.tsx` — Add Settings nav item
- `apps/dashboard/hooks/queries.ts` — Add Kakao status query hook

### Definition of Done
- [ ] `cd backend && go test ./...` → all tests pass (including new Kakao tests)
- [ ] `cd backend && go build -o /dev/null ./cmd/omo-core` → builds successfully
- [ ] Backend starts without Kakao env vars → logs "Kakao notifier not configured" or no Kakao log lines
- [ ] Migration 015 runs idempotently (`CREATE TABLE IF NOT EXISTS`)
- [ ] Settings page loads at `/settings` with Kakao connect/disconnect UI
- [ ] Existing Discord/Telegram notifications unaffected (regression check)

### Must Have
- KakaoNotifier implements `NotifierPort` exactly (no interface modifications)
- Auto token refresh on expired access token (transparent to caller)
- Graceful degradation: no tokens → return error → notify.Service logs warning and continues
- OAuth CSRF protection via HMAC-signed state parameter
- Daily rate limit counter (100/user/day) with log warning and skip
- Frontend Settings page with connect/disconnect/status display
- `SetBaseURL()` method for test injection (matching Telegram adapter pattern)

### Must NOT Have (Guardrails)
- DO NOT modify `ports/notifier.go` — Kakao's statefulness is internal to the adapter
- DO NOT modify `notification/multi.go` — Kakao plugs into existing fan-out
- DO NOT modify `app/notify/service.go` — message formatting stays generic
- DO NOT create a general settings/preferences framework — Kakao-specific page only
- DO NOT add user authentication or session management beyond OAuth state parameter
- DO NOT add message template configuration or customization — hardcoded text template
- DO NOT encrypt tokens — plaintext in DB (match existing patterns)
- DO NOT add rate limiting middleware — simple counter inside Kakao adapter
- DO NOT batch or aggregate messages — simple skip-and-log when limit approached
- DO NOT refactor notification config structure — add flat fields matching existing pattern

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES — `go test ./...` runs from project root
- **Automated tests**: Tests-after (matching existing adapter test pattern)
- **Framework**: Go stdlib `testing` + `net/http/httptest`

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend adapter/handler**: Bash (go test, curl, go build)
- **Frontend UI**: Playwright (navigate, click, assert DOM, screenshot)
- **Migration**: Bash (psql commands)

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation — all independent, start immediately):
├── T1: Config + dependency (add Kakao fields, x/oauth2) [quick]
├── T2: Database migration (015_create_kakao_tokens) [quick]
├── T3: Token port + entity types [quick]
├── T4: Kakao template builder [quick]
└── T5: OAuth HMAC state utility [quick]

Wave 2 (Core Backend — depends on Wave 1):
├── T6: Token repository (TimescaleDB) (depends: T2, T3) [unspecified-high]
├── T7: KakaoNotifier adapter (depends: T1, T3, T4) [deep]
├── T8: OAuth HTTP handler (depends: T1, T3, T5, T6) [unspecified-high]
├── T9: Adapter + template tests (depends: T4, T7) [quick]
└── T10: Repo + handler + state tests (depends: T5, T6, T8) [quick]

Wave 3 (Integration + Frontend — depends on Wave 2):
├── T11: main.go wiring + handler registration (depends: T6, T7, T8) [unspecified-high]
├── T12: Frontend Settings page + API routes + sidebar (depends: T8 for API contract) [visual-engineering]
└── T13: Backend build + regression verification (depends: T11) [quick]

Wave FINAL (Verification — after ALL tasks, 4 parallel):
├── F1: Plan compliance audit [oracle]
├── F2: Code quality review [unspecified-high]
├── F3: Real QA (Playwright + curl) [unspecified-high]
└── F4: Scope fidelity check [deep]

Critical Path: T1 → T7 → T11 → T13 → F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 5 (Wave 1)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| T1 | — | T7, T8, T11 | 1 |
| T2 | — | T6 | 1 |
| T3 | — | T6, T7, T8 | 1 |
| T4 | — | T7, T9 | 1 |
| T5 | — | T8, T10 | 1 |
| T6 | T2, T3 | T8, T10, T11 | 2 |
| T7 | T1, T3, T4 | T9, T11 | 2 |
| T8 | T1, T3, T5, T6 | T10, T11, T12 | 2 |
| T9 | T4, T7 | T13 | 2 |
| T10 | T5, T6, T8 | T13 | 2 |
| T11 | T6, T7, T8 | T13 | 3 |
| T12 | T8 (API contract) | F3 | 3 |
| T13 | T11, T9, T10 | F1-F4 | 3 |
| F1-F4 | T13 | — | Final |

### Agent Dispatch Summary

- **Wave 1**: 5 tasks — T1-T5 → `quick`
- **Wave 2**: 5 tasks — T6 → `unspecified-high`, T7 → `deep`, T8 → `unspecified-high`, T9 → `quick`, T10 → `quick`
- **Wave 3**: 3 tasks — T11 → `unspecified-high`, T12 → `visual-engineering`, T13 → `quick`
- **Wave FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [ ] 1. Config + Dependency Setup

  **What to do**:
  - Add Kakao fields to `NotificationConfig` struct in `backend/internal/config/config.go`:
    - `KakaoRestAPIKey string` — Kakao REST API Key (app credential, NOT the user token)
    - `KakaoClientSecret string` — Kakao Client Secret (optional, for enhanced security)
    - `KakaoRedirectURI string` — OAuth2 redirect URI pointing to backend callback
    - `KakaoLinkURL string` — URL for mandatory `link.web_url` in message templates
  - Add env overlay section (matching existing pattern at lines 360-368):
    - `KAKAO_REST_API_KEY`, `KAKAO_CLIENT_SECRET`, `KAKAO_REDIRECT_URI`, `KAKAO_LINK_URL`
  - Add default for `KakaoLinkURL` as `"https://developers.kakao.com"` in the defaults section
  - Update `configs/config.yaml` with commented-out Kakao fields (matching lines 66-69 pattern)
  - Add `golang.org/x/oauth2` to `go.mod`: run `go get golang.org/x/oauth2`
  - Do NOT add validation for Kakao fields (notifications are optional, matching existing pattern)

  **Must NOT do**:
  - Do not restructure NotificationConfig into a nested/complex structure
  - Do not add validation that would make Kakao config required
  - Do not modify any other config sections

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Small modifications to existing files + one dependency addition
  - **Skills**: []
    - No specialized skills needed for config field additions

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2, T3, T4, T5)
  - **Blocks**: T7 (KakaoNotifier needs config), T8 (OAuth handler needs config), T11 (wiring needs config)
  - **Blocked By**: None (can start immediately)

  **References**:

  **Pattern References**:
  - `backend/internal/config/config.go:76-80` — Existing `NotificationConfig` struct with flat string fields (TelegramBotToken, TelegramChatID, DiscordWebhookURL). Add Kakao fields in the same style
  - `backend/internal/config/config.go:360-368` — Env overlay pattern for notification fields. Add `KAKAO_*` overlays in the same if-val block style
  - `backend/internal/config/config.go:188` — `rawConfig` struct also has `NotificationConfig` field — no modification needed since it uses the same type

  **External References**:
  - `configs/config.yaml:66-69` — YAML notification section with commented-out env var hints. Add Kakao comments in same format

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Backend compiles with new config fields
    Tool: Bash
    Preconditions: Wave 1 tasks complete
    Steps:
      1. Run `cd backend && go build -o /dev/null ./cmd/omo-core`
      2. Assert exit code 0
    Expected Result: Build succeeds with no errors
    Failure Indicators: Compilation errors mentioning config fields
    Evidence: .sisyphus/evidence/task-1-build.txt

  Scenario: Env vars load into config correctly
    Tool: Bash
    Preconditions: Backend builds
    Steps:
      1. Run `cd backend && go test ./internal/config/ -v -run TestLoad` (if test exists) or verify by grep that env overlay code compiles
      2. Verify `KAKAO_REST_API_KEY`, `KAKAO_CLIENT_SECRET`, `KAKAO_REDIRECT_URI`, `KAKAO_LINK_URL` all have overlay blocks
    Expected Result: All 4 env overlay blocks present in config.go
    Failure Indicators: Missing env overlay for any Kakao field
    Evidence: .sisyphus/evidence/task-1-env-overlay.txt

  Scenario: golang.org/x/oauth2 dependency added
    Tool: Bash
    Preconditions: go get completed
    Steps:
      1. Run `cd backend && grep "golang.org/x/oauth2" go.mod`
      2. Assert output contains the dependency line
    Expected Result: go.mod contains `golang.org/x/oauth2` entry
    Failure Indicators: grep returns empty
    Evidence: .sisyphus/evidence/task-1-gomod.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(notification): add kakao foundation (config, migration, ports, template, state)`
  - Files: `backend/internal/config/config.go`, `configs/config.yaml`, `backend/go.mod`, `backend/go.sum`
  - Pre-commit: `cd backend && go build -o /dev/null ./cmd/omo-core`

- [ ] 2. Database Migration — `015_create_kakao_tokens`

  **What to do**:
  - Create `migrations/015_create_kakao_tokens.up.sql`:
    ```sql
    CREATE TABLE IF NOT EXISTS kakao_tokens (
        tenant_id       TEXT PRIMARY KEY DEFAULT 'default',
        access_token    TEXT NOT NULL,
        refresh_token   TEXT NOT NULL,
        token_type      TEXT NOT NULL DEFAULT 'Bearer',
        expires_at      TIMESTAMPTZ NOT NULL,
        refresh_expires_at TIMESTAMPTZ,
        scope           TEXT,
        created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );
    -- TODO: encrypt access_token and refresh_token at rest
    ```
  - Create `migrations/015_create_kakao_tokens.down.sql`:
    ```sql
    DROP TABLE IF EXISTS kakao_tokens;
    ```
  - Use `tenant_id` as PRIMARY KEY (single-operator MVP but future-proof for multi-tenant)
  - Include `refresh_expires_at` to enable proactive expiry alerting
  - Use `TIMESTAMPTZ` for all timestamps (matching existing migration patterns)

  **Must NOT do**:
  - Do not add encryption columns or crypto functions
  - Do not create indexes (primary key is sufficient for single-row table)
  - Do not add foreign key references to other tables

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Two small SQL files following established migration pattern
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T3, T4, T5)
  - **Blocks**: T6 (token repository needs table schema)
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `migrations/014_create_dna_approvals.up.sql` — Latest migration showing `CREATE TABLE IF NOT EXISTS`, `TEXT PRIMARY KEY`, `TIMESTAMPTZ NOT NULL DEFAULT NOW()` patterns
  - `migrations/014_create_dna_approvals.down.sql` — Down migration pattern (`DROP TABLE IF EXISTS`)

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Up migration runs without error
    Tool: Bash
    Preconditions: TimescaleDB running, DATABASE_URL set
    Steps:
      1. Run `psql "$DATABASE_URL" -f migrations/015_create_kakao_tokens.up.sql`
      2. Assert exit code 0
    Expected Result: Table created successfully
    Failure Indicators: SQL syntax error or constraint violation
    Evidence: .sisyphus/evidence/task-2-migration-up.txt

  Scenario: Migration is idempotent (run up twice)
    Tool: Bash
    Preconditions: Up migration already run once
    Steps:
      1. Run `psql "$DATABASE_URL" -f migrations/015_create_kakao_tokens.up.sql` again
      2. Assert exit code 0 (no error due to IF NOT EXISTS)
    Expected Result: No error on second run
    Failure Indicators: "table already exists" error
    Evidence: .sisyphus/evidence/task-2-idempotent.txt

  Scenario: Down migration drops table
    Tool: Bash
    Preconditions: Table exists
    Steps:
      1. Run `psql "$DATABASE_URL" -f migrations/015_create_kakao_tokens.down.sql`
      2. Run `psql "$DATABASE_URL" -c "SELECT 1 FROM kakao_tokens LIMIT 1"` and expect error
    Expected Result: Table no longer exists
    Failure Indicators: Table still queryable after down migration
    Evidence: .sisyphus/evidence/task-2-migration-down.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Files: `migrations/015_create_kakao_tokens.up.sql`, `migrations/015_create_kakao_tokens.down.sql`

- [ ] 3. Token Port Interface + Entity Types

  **What to do**:
  - Create `backend/internal/ports/kakao_token.go`:
    - Define `KakaoTokenStore` interface:
      ```go
      type KakaoTokenStore interface {
          SaveToken(ctx context.Context, tenantID string, token *KakaoToken) error
          GetToken(ctx context.Context, tenantID string) (*KakaoToken, error)
          DeleteToken(ctx context.Context, tenantID string) error
      }
      ```
    - Define `KakaoToken` struct in the same file:
      ```go
      type KakaoToken struct {
          TenantID         string
          AccessToken      string
          RefreshToken     string
          TokenType        string
          ExpiresAt        time.Time
          RefreshExpiresAt *time.Time  // nullable — Kakao may not always return this
          Scope            string
          UpdatedAt        time.Time
      }
      ```
  - Keep the port in `ports/` package (matching hexagonal architecture — port is a boundary)
  - The entity is co-located with the port (it's a simple data structure, not domain logic)

  **Must NOT do**:
  - Do not modify `ports/notifier.go`
  - Do not add methods beyond Save/Get/Delete (YAGNI)
  - Do not add domain validation logic to the entity

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single small file with interface + struct definition
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T4, T5)
  - **Blocks**: T6 (repo implements this), T7 (adapter uses this), T8 (handler uses this)
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/internal/ports/notifier.go` — Existing port interface pattern (single file, minimal, `context.Context` first parameter)
  - `backend/internal/ports/repository.go` — Repository port showing how data access interfaces are defined

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Port file compiles
    Tool: Bash
    Preconditions: File created
    Steps:
      1. Run `cd backend && go build ./internal/ports/`
      2. Assert exit code 0
    Expected Result: Package compiles with no errors
    Failure Indicators: Type errors, missing imports
    Evidence: .sisyphus/evidence/task-3-compile.txt

  Scenario: Interface is usable (compile-time check)
    Tool: Bash
    Preconditions: Port file exists
    Steps:
      1. Verify the interface can be used as a dependency type (will be validated when T6 implements it)
      2. Run `cd backend && go vet ./internal/ports/`
    Expected Result: No vet warnings
    Failure Indicators: Vet errors
    Evidence: .sisyphus/evidence/task-3-vet.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Files: `backend/internal/ports/kakao_token.go`

- [ ] 4. Kakao Message Template Builder

  **What to do**:
  - Create `backend/internal/adapters/notification/kakao_template.go`:
    - Function `buildTemplateObject(message, linkURL string) (string, error)` that:
      1. Takes a plain text message (same format Discord/Telegram receive)
      2. Constructs the Kakao JSON template: `{"object_type":"text","text":"<message>","link":{"web_url":"<linkURL>"}}`
      3. URL-encodes the JSON string
      4. Returns `"template_object=<url-encoded-json>"` as the form body string
    - Handle message truncation: Kakao text field may have length limits — truncate with `"..."` suffix if over 1000 chars (conservative limit; actual limit TBD from API response testing)
    - Include `buildFormBody(message, linkURL string) (string, error)` as the public API
  - Create `backend/internal/adapters/notification/kakao_template_test.go`:
    - Test happy path: message + link → correct form body
    - Test special characters: Korean, emoji, HTML entities in message
    - Test long message truncation
    - Test empty link URL falls back to default

  **Must NOT do**:
  - Do not support multiple template types (list, commerce, etc.) — text only
  - Do not add template configuration or customization
  - Do not create a template builder interface — simple package-level functions

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Pure function, no external dependencies, straightforward encoding logic
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T3, T5)
  - **Blocks**: T7 (KakaoNotifier uses template builder), T9 (template tests)
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/notification/discord.go:30-32` — Payload struct pattern (`discordPayload`)
  - `backend/internal/adapters/notification/telegram.go:39-43` — Payload struct pattern (`telegramPayload`)

  **External References**:
  - KakaoTalk Memo API docs: The `template_object` must be a URL-encoded JSON string with `object_type`, `text`, and `link` fields
  - Content-Type for the API call is `application/x-www-form-urlencoded` — the template builder produces the form body, NOT JSON

  **WHY Each Reference Matters**:
  - Discord/Telegram payload structs show how the project structures API payloads — Kakao is different (form-encoded not JSON) but the organizational pattern applies
  - The template builder MUST produce form-encoded body, not JSON — this is the #1 implementation trap identified by Metis

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Template builder produces correct form body
    Tool: Bash
    Preconditions: File created
    Steps:
      1. Run `cd backend && go test ./internal/adapters/notification/ -run TestBuildFormBody -v`
      2. Assert test passes with correct URL-encoded output
    Expected Result: All template tests pass
    Failure Indicators: URL encoding errors, malformed JSON in template_object
    Evidence: .sisyphus/evidence/task-4-template-test.txt

  Scenario: Template handles emoji and Korean characters
    Tool: Bash
    Preconditions: Test includes Unicode test case
    Steps:
      1. Run template builder with message containing "📤 주문 제출: AAPL"
      2. Assert output correctly URL-encodes the Unicode
    Expected Result: UTF-8 characters properly encoded in form body
    Failure Indicators: Encoding errors, garbled output
    Evidence: .sisyphus/evidence/task-4-unicode.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Files: `backend/internal/adapters/notification/kakao_template.go`, `backend/internal/adapters/notification/kakao_template_test.go`

- [ ] 5. OAuth HMAC State Utility

  **What to do**:
  - Create `backend/internal/adapters/http/kakao_state.go`:
    - Function `GenerateState(secret []byte) (string, error)` that:
      1. Generates 16 random bytes (nonce)
      2. Gets current Unix timestamp
      3. Creates payload: `timestamp:nonce`
      4. Signs with HMAC-SHA256 using `secret`
      5. Returns base64url-encoded `payload.signature` string
    - Function `ValidateState(state string, secret []byte, maxAge time.Duration) error` that:
      1. Splits into payload + signature
      2. Recomputes HMAC and verifies (constant-time compare)
      3. Checks timestamp is within `maxAge` (recommend 10 minutes)
      4. Returns descriptive error if validation fails
  - Create `backend/internal/adapters/http/kakao_state_test.go`:
    - Test valid state generation and verification
    - Test expired state rejection
    - Test tampered state rejection
    - Test wrong secret rejection

  **Must NOT do**:
  - Do not create a session store or cookie management system
  - Do not use external session libraries
  - Do not store state server-side — it's self-contained via HMAC

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Small, self-contained crypto utility with clear test cases
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T3, T4)
  - **Blocks**: T8 (OAuth handler uses state utility), T10 (state tests)
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - No existing pattern in codebase — this is a new utility. Follow Go stdlib crypto patterns: `crypto/hmac`, `crypto/sha256`, `crypto/rand`, `encoding/base64`

  **External References**:
  - OAuth 2.0 `state` parameter specification (RFC 6749 Section 10.12) — CSRF protection via unguessable value

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: State generation and validation round-trips
    Tool: Bash
    Preconditions: File created
    Steps:
      1. Run `cd backend && go test ./internal/adapters/http/ -run TestState -v`
      2. Assert all state tests pass
    Expected Result: Generate → Validate succeeds, tampered/expired/wrong-secret fails
    Failure Indicators: HMAC verification failure on valid state, or acceptance of tampered state
    Evidence: .sisyphus/evidence/task-5-state-test.txt

  Scenario: Expired state is rejected
    Tool: Bash
    Preconditions: Test creates state, then validates with maxAge=0
    Steps:
      1. Run specific expired-state test case
      2. Assert error returned
    Expected Result: Error indicating state has expired
    Failure Indicators: Expired state accepted
    Evidence: .sisyphus/evidence/task-5-expiry.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Files: `backend/internal/adapters/http/kakao_state.go`, `backend/internal/adapters/http/kakao_state_test.go`

- [ ] 6. Token Repository — TimescaleDB Implementation

  **What to do**:
  - Create `backend/internal/adapters/timescaledb/kakao_token_repo.go`:
    - Struct `KakaoTokenRepo` with `db DBTX` and `log zerolog.Logger` fields
    - Constructor `NewKakaoTokenRepo(db DBTX, log zerolog.Logger) *KakaoTokenRepo`
    - Port compliance check: `var _ ports.KakaoTokenStore = (*KakaoTokenRepo)(nil)`
    - Implement `SaveToken(ctx, tenantID, token)` — UPSERT (INSERT ON CONFLICT UPDATE)
    - Implement `GetToken(ctx, tenantID)` — SELECT, return `nil, nil` if not found (not an error)
    - Implement `DeleteToken(ctx, tenantID)` — DELETE
    - SQL constants at package level (matching existing repo pattern)
    - All methods use `context.Context` for cancellation
    - Error wrapping with `"kakao_token_repo: "` prefix
  - Create `backend/internal/adapters/timescaledb/kakao_token_repo_test.go`:
    - Test SQL compilation (verify queries don't have syntax errors by compiling)
    - If a test DB is available, test Save → Get → Delete round trip

  **Must NOT do**:
  - Do not add encryption/decryption logic
  - Do not add caching — direct DB access each time
  - Do not add methods beyond Save/Get/Delete

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Database repository with SQL, UPSERT logic, and interface compliance
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T7, T8, T9, T10)
  - **Blocks**: T8 (OAuth handler stores tokens), T10 (repo tests), T11 (wiring needs repo)
  - **Blocked By**: T2 (needs table schema), T3 (needs port interface)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/timescaledb/dna_approval_repo.go` — Exemplar repo: SQL constants at package level, `DBTX` interface, `NewXxxRepo(db, log)` constructor, port compliance check `var _ ports.XxxPort = (*Repo)(nil)`
  - `backend/internal/adapters/timescaledb/db.go:24-28` — `DBTX` interface for testability

  **API/Type References**:
  - `backend/internal/ports/kakao_token.go` (T3) — `KakaoTokenStore` interface to implement, `KakaoToken` struct to marshal/unmarshal

  **WHY Each Reference Matters**:
  - `dna_approval_repo.go` is the canonical DB repo pattern — SQL at top, DBTX injection, zerolog, error wrapping. Follow it exactly
  - `db.go` DBTX interface enables testing without real DB connection

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Repo compiles and satisfies port interface
    Tool: Bash
    Preconditions: T2 (migration) and T3 (port) complete
    Steps:
      1. Run `cd backend && go build ./internal/adapters/timescaledb/`
      2. Assert exit code 0 — the `var _ ports.KakaoTokenStore = (*KakaoTokenRepo)(nil)` line will fail at compile time if interface isn't satisfied
    Expected Result: Compiles with no errors
    Failure Indicators: Interface compliance failure
    Evidence: .sisyphus/evidence/task-6-compile.txt

  Scenario: SQL queries are syntactically valid
    Tool: Bash
    Preconditions: Repo file created
    Steps:
      1. Run `cd backend && go vet ./internal/adapters/timescaledb/`
      2. Visually verify SQL constants match migration schema columns
    Expected Result: No vet errors, SQL matches schema
    Failure Indicators: Column name mismatches between SQL and migration
    Evidence: .sisyphus/evidence/task-6-vet.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: `feat(notification): implement kakao adapter, token repo, oauth handler with tests`
  - Files: `backend/internal/adapters/timescaledb/kakao_token_repo.go`, `backend/internal/adapters/timescaledb/kakao_token_repo_test.go`

- [ ] 7. KakaoNotifier Adapter — Implements NotifierPort

  **What to do**:
  - Create `backend/internal/adapters/notification/kakao.go`:
    - Struct `KakaoNotifier`:
      ```go
      type KakaoNotifier struct {
          tokenStore ports.KakaoTokenStore
          tenantID   string          // which tenant's tokens to use (default: "default")
          linkURL    string          // mandatory link.web_url in message template
          client     *http.Client
          baseURL    string          // https://kapi.kakao.com (overridable for testing)
          dailySent  int             // daily send counter for rate limiting
          dailyDate  string          // date string for counter reset (YYYY-MM-DD)
          dailyLimit int             // max sends per day (default: 90, leaving buffer from 100 limit)
          mu         sync.Mutex      // protects dailySent/dailyDate
          log        zerolog.Logger
      }
      ```
    - Constructor `NewKakaoNotifier(tokenStore ports.KakaoTokenStore, tenantID, linkURL string, client *http.Client, log zerolog.Logger) *KakaoNotifier`
    - `SetBaseURL(baseURL string)` method — for test injection (matching Telegram pattern)
    - `Notify(ctx context.Context, tenantID, message string) error`:
      1. Check daily rate limit counter → if at limit, log warning and return error
      2. Get token from `tokenStore.GetToken(ctx, n.tenantID)` → if nil, return `fmt.Errorf("kakao: not connected (no tokens)")`
      3. Check if access token is expired → if yes, refresh using `golang.org/x/oauth2` token refresh
      4. Build form body using `buildFormBody(message, n.linkURL)` (from T4)
      5. POST to `{baseURL}/v2/api/talk/memo/default/send` with `Content-Type: application/x-www-form-urlencoded` and `Authorization: Bearer {access_token}`
      6. If 401 response → attempt token refresh → retry once
      7. If refresh succeeds → save new token to store → retry send
      8. If refresh fails → return error (notify.Service will log-warn and continue)
      9. Increment daily counter on success
    - Private method `refreshToken(ctx context.Context, token *ports.KakaoToken) (*ports.KakaoToken, error)` — uses `golang.org/x/oauth2` `TokenSource`
    - Error wrapping with `"kakao: "` prefix (matching Discord's `"discord: "` and Telegram's `"telegram: "` prefixes)

  **CRITICAL IMPLEMENTATION NOTES**:
  - Content-Type is `application/x-www-form-urlencoded` NOT `application/json` — this is the #1 trap
  - Token refresh must be thread-safe (multiple events can fire concurrently)
  - The `tenantID` parameter in `Notify()` is the event's tenant, but `n.tenantID` is which token set to use (single-operator mode)
  - Daily counter resets when `time.Now().Format("2006-01-02")` changes — simple, no timezone complexity needed

  **Must NOT do**:
  - Do not modify `NotifierPort` interface
  - Do not add message batching or aggregation
  - Do not add sophisticated rate limiting (no sliding window, no token bucket)
  - Do not add retry logic beyond the single 401→refresh→retry cycle

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Most complex task — OAuth2 token refresh, thread-safety, form encoding, retry logic, rate limiting. Core adapter that must be correct
  - **Skills**: [`senior-backend`]
    - `senior-backend`: OAuth2 token management, concurrent access patterns, HTTP client best practices

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T6, T8, T9, T10)
  - **Blocks**: T9 (adapter tests), T11 (wiring)
  - **Blocked By**: T1 (config for linkURL default), T3 (port interface), T4 (template builder)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/notification/discord.go` — Adapter structure: struct with credentials + `*http.Client`, constructor with nil-client fallback, `Notify()` marshals payload + HTTP POST + status check + error wrapping
  - `backend/internal/adapters/notification/telegram.go:34-36` — `SetBaseURL()` pattern for test injection
  - `backend/internal/adapters/notification/telegram.go:46-75` — `Notify()` flow: build payload → marshal → create request → set headers → do request → check status → return error

  **API/Type References**:
  - `backend/internal/ports/notifier.go:8-9` — `NotifierPort` interface this must implement
  - `backend/internal/ports/kakao_token.go` (T3) — `KakaoTokenStore` and `KakaoToken` types
  - `backend/internal/adapters/notification/kakao_template.go` (T4) — `buildFormBody()` function

  **External References**:
  - KakaoTalk Memo API: `POST https://kapi.kakao.com/v2/api/talk/memo/default/send` with `Authorization: Bearer <token>` and `Content-Type: application/x-www-form-urlencoded`
  - `golang.org/x/oauth2` package — `oauth2.Config`, `oauth2.Token`, `TokenSource` for thread-safe refresh

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: KakaoNotifier compiles and satisfies NotifierPort
    Tool: Bash
    Preconditions: T1, T3, T4 complete
    Steps:
      1. Run `cd backend && go build ./internal/adapters/notification/`
      2. Check for `var _ ports.NotifierPort = (*KakaoNotifier)(nil)` in the file
    Expected Result: Compiles, implements NotifierPort
    Failure Indicators: Interface satisfaction failure
    Evidence: .sisyphus/evidence/task-7-compile.txt

  Scenario: Notify sends form-encoded POST (not JSON)
    Tool: Bash
    Preconditions: T9 test file exists
    Steps:
      1. Run `cd backend && go test ./internal/adapters/notification/ -run TestKakaoNotifier_Notify_Success -v`
      2. Assert test verifies Content-Type header is `application/x-www-form-urlencoded`
      3. Assert test verifies body contains `template_object=` prefix
    Expected Result: Form-encoded request sent correctly
    Failure Indicators: JSON Content-Type, missing template_object
    Evidence: .sisyphus/evidence/task-7-content-type.txt

  Scenario: Rate limit prevents excessive sends
    Tool: Bash
    Preconditions: T9 test file exists
    Steps:
      1. Run test that calls Notify() 91+ times
      2. Assert sends stop at daily limit with descriptive error
    Expected Result: Error returned after limit reached
    Failure Indicators: Sends continue beyond limit
    Evidence: .sisyphus/evidence/task-7-rate-limit.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Files: `backend/internal/adapters/notification/kakao.go`

- [ ] 8. OAuth HTTP Handler — Backend Endpoints

  **What to do**:
  - Create `backend/internal/adapters/http/kakao_auth.go`:
    - Struct `KakaoAuthHandler` with fields: `tokenStore ports.KakaoTokenStore`, `oauthConfig oauth2.Config`, `stateSecret []byte`, `log zerolog.Logger`
    - Constructor `NewKakaoAuthHandler(tokenStore, restAPIKey, clientSecret, redirectURI string, stateSecret []byte, log zerolog.Logger) *KakaoAuthHandler`
    - Implements `http.Handler` with `ServeHTTP` routing to sub-handlers based on path suffix
    - Four endpoints under `/auth/kakao/`:
      1. `GET /auth/kakao/start` → Redirect to Kakao OAuth authorization URL with HMAC state parameter and `scope=talk_message`
      2. `GET /auth/kakao/callback` → Receive authorization code, validate state, exchange code for tokens, save to `tokenStore`, redirect to dashboard `/settings?kakao=connected`
      3. `GET /auth/kakao/status` → Return JSON: `{"connected": bool, "expires_at": "...", "refresh_expires_at": "...", "daily_sends": N}`
      4. `POST /auth/kakao/disconnect` → Delete tokens from store, return `{"ok": true}`
    - OAuth2 config setup:
      ```go
      oauth2.Config{
          ClientID:     restAPIKey,
          ClientSecret: clientSecret,
          Endpoint: oauth2.Endpoint{
              AuthURL:  "https://kauth.kakao.com/oauth/authorize",
              TokenURL: "https://kauth.kakao.com/oauth/token",
          },
          RedirectURL: redirectURI,
          Scopes:      []string{"talk_message"},
      }
      ```
    - CORS headers on status and disconnect endpoints (matching existing inline CORS pattern)
    - Error responses as JSON: `{"error": "description"}`

  **Must NOT do**:
  - Do not add session management or cookies (beyond OAuth state parameter)
  - Do not add user authentication — this is operator-level access
  - Do not add CSRF middleware — HMAC state parameter is sufficient for OAuth flow
  - Do not add rate limiting on auth endpoints

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: OAuth2 flow with state validation, token exchange, multiple endpoints
  - **Skills**: [`senior-backend`]
    - `senior-backend`: OAuth2 flow implementation, HTTP handler patterns, security considerations

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T6, T7, T9, T10)
  - **Blocks**: T10 (handler tests), T11 (wiring), T12 (frontend needs API contract)
  - **Blocked By**: T1 (config), T3 (token port), T5 (state utility), T6 (token repo for storage)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/http/` — HTTP handler files (examine existing patterns for handler struct + ServeHTTP routing)
  - `backend/cmd/omo-core/main.go:626-639` — Inline CORS headers pattern: `w.Header().Set("Access-Control-Allow-Origin", "*")` etc.
  - `backend/cmd/omo-core/main.go:620-625` — `imux.Handle("/path", handler)` registration pattern
  - `backend/cmd/omo-core/main.go:662-663` — Handler registration with path prefix: `imux.Handle("/api/dna/", dnaApprovalHandler)`

  **API/Type References**:
  - `backend/internal/ports/kakao_token.go` (T3) — `KakaoTokenStore` interface for token persistence
  - `backend/internal/adapters/http/kakao_state.go` (T5) — `GenerateState()` and `ValidateState()` functions

  **External References**:
  - Kakao OAuth2 flow: `https://kauth.kakao.com/oauth/authorize?client_id={REST_API_KEY}&redirect_uri={URI}&response_type=code&scope=talk_message&state={STATE}`
  - Token exchange: `POST https://kauth.kakao.com/oauth/token` with `grant_type=authorization_code&client_id=...&redirect_uri=...&code=...`
  - `golang.org/x/oauth2` — `Config.AuthCodeURL()`, `Config.Exchange()`

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: /auth/kakao/start redirects to Kakao
    Tool: Bash
    Preconditions: Handler created with valid config
    Steps:
      1. Run handler test or curl `http://localhost:8080/auth/kakao/start`
      2. Assert HTTP 307 redirect
      3. Assert Location header contains `kauth.kakao.com/oauth/authorize`
      4. Assert Location contains `scope=talk_message`
      5. Assert Location contains `state=` parameter
    Expected Result: Redirect to Kakao OAuth with correct parameters
    Failure Indicators: Wrong redirect URL, missing scope, missing state
    Evidence: .sisyphus/evidence/task-8-redirect.txt

  Scenario: /auth/kakao/status returns disconnected state
    Tool: Bash
    Preconditions: No tokens in store
    Steps:
      1. GET /auth/kakao/status
      2. Assert response is `{"connected": false}`
    Expected Result: JSON showing not connected
    Failure Indicators: Error response, wrong JSON structure
    Evidence: .sisyphus/evidence/task-8-status-disconnected.txt

  Scenario: /auth/kakao/callback rejects invalid state
    Tool: Bash
    Preconditions: Handler created
    Steps:
      1. GET /auth/kakao/callback?code=test&state=tampered
      2. Assert HTTP 400 error response
    Expected Result: State validation fails, returns error
    Failure Indicators: Accepts tampered state, proceeds with token exchange
    Evidence: .sisyphus/evidence/task-8-invalid-state.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Files: `backend/internal/adapters/http/kakao_auth.go`, `backend/internal/adapters/http/kakao_auth_test.go`

- [ ] 9. KakaoNotifier + Template Tests

  **What to do**:
  - Create `backend/internal/adapters/notification/kakao_test.go` (if not already created in T4/T7):
    - `TestKakaoNotifier_Notify_Success` — httptest server simulating Kakao API, verify:
      - Request method is POST
      - Content-Type is `application/x-www-form-urlencoded` (NOT `application/json`)
      - Authorization header is `Bearer <access_token>`
      - Body contains `template_object=` with URL-encoded JSON
      - Message text appears in decoded template_object
    - `TestKakaoNotifier_Notify_NoTokens` — tokenStore returns nil → error with "not connected"
    - `TestKakaoNotifier_Notify_ExpiredToken_RefreshSuccess` — first call returns 401, token refresh succeeds, retry succeeds
    - `TestKakaoNotifier_Notify_RefreshFailure` — refresh attempt fails → error returned
    - `TestKakaoNotifier_Notify_RateLimit` — exceed daily limit → error with "rate limit" message
    - `TestKakaoNotifier_Notify_ContextCancellation` — cancelled context → error (matching discord/telegram test pattern)
    - `TestKakaoNotifier_ImplementsNotifierPort` — `var _ ports.NotifierPort = (*KakaoNotifier)(nil)` (matching multi_test.go pattern)
  - Use `httptest.NewServer` with handler that simulates Kakao responses (matching existing test patterns)
  - Create mock `KakaoTokenStore` implementation for tests
  - Ensure kakao_template_test.go (from T4) is complete

  **Must NOT do**:
  - Do not make real HTTP calls to Kakao API
  - Do not require database for tests

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Test files following established patterns — httptest, assertions, mocks
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: Mock strategies and test organization

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T6, T7, T8, T10)
  - **Blocks**: T13 (build verification)
  - **Blocked By**: T4 (template builder), T7 (KakaoNotifier implementation)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/notification/discord_test.go` — Exact test pattern to follow: httptest.NewServer, capture body/method/headers, assert response
  - `backend/internal/adapters/notification/telegram_test.go:15-49` — Success test pattern with captured body, path, and field assertions
  - `backend/internal/adapters/notification/telegram_test.go:67-83` — Context cancellation test pattern
  - `backend/internal/adapters/notification/multi_test.go:13-25` — stubNotifier pattern for mock implementations

  **Test References**:
  - `backend/internal/adapters/notification/discord_test.go:81-101` — Verifying HTTP method and Content-Type headers

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: All Kakao notification tests pass
    Tool: Bash
    Preconditions: T4, T7 complete
    Steps:
      1. Run `cd backend && go test ./internal/adapters/notification/ -run TestKakao -v`
      2. Assert all tests pass
      3. Count test functions — expect ≥7
    Expected Result: All tests PASS
    Failure Indicators: Any FAIL, fewer than 7 tests
    Evidence: .sisyphus/evidence/task-9-tests.txt

  Scenario: Content-Type assertion specifically verified
    Tool: Bash
    Preconditions: Tests exist
    Steps:
      1. Grep test file for "x-www-form-urlencoded"
      2. Assert at least one test verifies this header
    Expected Result: Content-Type verification present in tests
    Failure Indicators: No Content-Type assertion (leaves the #1 trap uncovered)
    Evidence: .sisyphus/evidence/task-9-content-type-test.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Files: `backend/internal/adapters/notification/kakao_test.go`

- [ ] 10. Token Repo + OAuth Handler + State Utility Tests

  **What to do**:
  - Ensure `backend/internal/adapters/http/kakao_state_test.go` (from T5) has comprehensive tests:
    - `TestGenerateState_ProducesValidFormat` — output is base64url-encoded, contains separator
    - `TestValidateState_AcceptsValid` — fresh state with correct secret validates
    - `TestValidateState_RejectsExpired` — state with timestamp older than maxAge rejected
    - `TestValidateState_RejectsTampered` — modified payload rejected
    - `TestValidateState_RejectsWrongSecret` — different secret rejected
  - Create `backend/internal/adapters/http/kakao_auth_test.go`:
    - `TestKakaoAuthHandler_Start_Redirects` — GET /auth/kakao/start returns 307 to Kakao
    - `TestKakaoAuthHandler_Callback_InvalidState` — bad state parameter returns 400
    - `TestKakaoAuthHandler_Status_NoTokens` — returns `{"connected": false}`
    - `TestKakaoAuthHandler_Status_WithTokens` — returns `{"connected": true, ...}`
    - `TestKakaoAuthHandler_Disconnect` — POST deletes tokens, returns `{"ok": true}`
    - Use mock `KakaoTokenStore` and httptest for Kakao token endpoint simulation
  - Ensure `backend/internal/adapters/timescaledb/kakao_token_repo_test.go` (from T6) verifies:
    - SQL constant strings compile (no syntax-level issues detectable in Go tests)
    - If possible with test DB: Save → Get round trip, Delete removes, Get after delete returns nil

  **Must NOT do**:
  - Do not require real Kakao API access for tests
  - Do not add integration test infrastructure (keep unit test level)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Test files following established patterns
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: httptest patterns, mock strategies

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with T6, T7, T8, T9)
  - **Blocks**: T13 (build verification)
  - **Blocked By**: T5 (state utility), T6 (token repo), T8 (OAuth handler)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/notification/telegram_test.go` — httptest server pattern for HTTP handler testing
  - `backend/internal/adapters/notification/multi_test.go:13-25` — Mock/stub pattern implementing port interface

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: All HTTP handler and state tests pass
    Tool: Bash
    Preconditions: T5, T6, T8 complete
    Steps:
      1. Run `cd backend && go test ./internal/adapters/http/ -run TestKakao -v`
      2. Assert all tests pass
    Expected Result: All handler + state tests PASS
    Failure Indicators: Any FAIL
    Evidence: .sisyphus/evidence/task-10-handler-tests.txt

  Scenario: Token repo tests compile
    Tool: Bash
    Preconditions: T6 complete
    Steps:
      1. Run `cd backend && go test ./internal/adapters/timescaledb/ -run TestKakaoToken -v` or `go build ./internal/adapters/timescaledb/`
      2. Assert compiles (tests may skip if no DB connection)
    Expected Result: Compiles, tests pass or skip gracefully
    Failure Indicators: Compilation failure
    Evidence: .sisyphus/evidence/task-10-repo-tests.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Files: `backend/internal/adapters/http/kakao_auth_test.go`, `backend/internal/adapters/timescaledb/kakao_token_repo_test.go`

- [ ] 11. main.go Wiring + OAuth Handler Registration

  **What to do**:
  - Modify `backend/cmd/omo-core/main.go`:
    - **Import** the new packages: `omhttp "github.com/oh-my-opentrade/backend/internal/adapters/http"` (may already exist), `timescaledb` adapter
    - **After line 105** (where dnaApprovalRepo is created), add:
      ```go
      kakaoTokenRepo := timescaledb.NewKakaoTokenRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "kakao_token_repo").Logger())
      ```
    - **In the notification section (lines 167-180)**, add Kakao notifier creation:
      ```go
      if cfg.Notification.KakaoRestAPIKey != "" {
          // Check if valid tokens exist in DB
          existingToken, err := kakaoTokenRepo.GetToken(context.Background(), "default")
          if err != nil {
              log.Warn().Err(err).Msg("failed to check Kakao tokens")
          }
          if existingToken != nil {
              kakaoNotifier := notification.NewKakaoNotifier(
                  kakaoTokenRepo, "default", cfg.Notification.KakaoLinkURL,
                  nil, log.With().Str("component", "kakao").Logger(),
              )
              notifiers = append(notifiers, kakaoNotifier)
              log.Info().Msg("Kakao notifier enabled")
          } else {
              log.Info().Msg("Kakao configured but not connected — visit /settings to authorize")
          }
      }
      ```
    - **After HTTP mux setup (around line 620)**, register OAuth handler:
      ```go
      if cfg.Notification.KakaoRestAPIKey != "" {
          stateSecret := []byte(cfg.Notification.KakaoRestAPIKey) // use REST API key as HMAC secret
          kakaoAuthHandler := omhttp.NewKakaoAuthHandler(
              kakaoTokenRepo,
              cfg.Notification.KakaoRestAPIKey,
              cfg.Notification.KakaoClientSecret,
              cfg.Notification.KakaoRedirectURI,
              stateSecret,
              log.With().Str("component", "kakao_auth").Logger(),
          )
          imux.Handle("/auth/kakao/", kakaoAuthHandler)
          log.Info().Msg("Kakao OAuth endpoints registered")
      }
      ```
  - Follow the exact same conditional creation pattern used for Discord/Telegram (lines 169-176)
  - The "configured but no tokens" state must NOT crash or error — it logs info and continues

  **Must NOT do**:
  - Do not restructure existing notification wiring code
  - Do not add Kakao-specific startup validation
  - Do not modify the MultiNotifier creation (line 177) — just append to the same `notifiers` slice

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Modifying the main entry point requires careful integration with existing wiring patterns
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Application bootstrapping, dependency injection, configuration-driven wiring

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T12)
  - **Parallel Group**: Wave 3 (with T12, T13)
  - **Blocks**: T13 (build verification)
  - **Blocked By**: T6 (token repo), T7 (KakaoNotifier), T8 (OAuth handler)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/main.go:167-180` — Notification adapter conditional creation + MultiNotifier wiring. This is the EXACT pattern to follow: check config → create adapter → append to slice → log
  - `backend/cmd/omo-core/main.go:103-105` — Repository creation pattern: `timescaledb.NewXxxRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "xxx").Logger())`
  - `backend/cmd/omo-core/main.go:662-663` — HTTP handler registration: `imux.Handle("/path/", handler)`
  - `backend/cmd/omo-core/main.go:25` — Existing import of notification package

  **API/Type References**:
  - `backend/internal/adapters/notification/kakao.go` (T7) — `NewKakaoNotifier()` constructor signature
  - `backend/internal/adapters/http/kakao_auth.go` (T8) — `NewKakaoAuthHandler()` constructor signature
  - `backend/internal/adapters/timescaledb/kakao_token_repo.go` (T6) — `NewKakaoTokenRepo()` constructor

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Backend builds with Kakao wiring
    Tool: Bash
    Preconditions: All Wave 1 + Wave 2 tasks complete
    Steps:
      1. Run `cd backend && go build -o /dev/null ./cmd/omo-core`
      2. Assert exit code 0
    Expected Result: Successful build
    Failure Indicators: Import errors, type mismatches, undefined symbols
    Evidence: .sisyphus/evidence/task-11-build.txt

  Scenario: Backend starts without Kakao env vars
    Tool: Bash
    Preconditions: Build succeeds
    Steps:
      1. Unset all KAKAO_* env vars
      2. Run `cd backend && timeout 3 go run ./cmd/omo-core/ 2>&1 | head -20`
      3. Assert no Kakao-related errors or panics
    Expected Result: Backend starts normally, possibly with "Kakao not configured" info log
    Failure Indicators: Panic, error log, startup failure
    Evidence: .sisyphus/evidence/task-11-no-config-start.txt

  Scenario: Backend starts with Kakao config but no tokens
    Tool: Bash
    Preconditions: KAKAO_REST_API_KEY set, but no tokens in DB
    Steps:
      1. Set KAKAO_REST_API_KEY=test-key
      2. Run `cd backend && timeout 3 go run ./cmd/omo-core/ 2>&1 | grep -i kakao`
      3. Assert log contains "configured but not connected" or similar
    Expected Result: Informational log, no error, OAuth endpoints registered
    Failure Indicators: Error, panic, notifier added without tokens
    Evidence: .sisyphus/evidence/task-11-no-tokens-start.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `feat(notification): wire kakao notifier, add settings page`
  - Files: `backend/cmd/omo-core/main.go`

- [ ] 12. Frontend — Settings Page + API Routes + Sidebar Navigation

  **What to do**:
  - **Add Settings nav item** to `apps/dashboard/components/sidebar.tsx`:
    - Import `Settings` icon from `lucide-react`
    - Add `{ href: "/settings", label: "Settings", icon: Settings }` to `navItems` array (after "Strategies")
  - **Create Settings page** at `apps/dashboard/app/settings/page.tsx`:
    - Page title "Settings" with card layout
    - KakaoTalk Notification section:
      - Connection status indicator (green dot = connected, gray = not connected)
      - If connected: show token expiry info, daily sends count, "Disconnect" button
      - If not connected: "Connect KakaoTalk" button that opens `{BACKEND_URL}/auth/kakao/start` in same window (full page redirect, Kakao will redirect back)
      - Disconnect button: POST to `/api/auth/kakao/disconnect`, invalidate status query on success
    - Match existing dashboard visual style: dark theme, card containers, muted text, emerald accents
    - Use TanStack Query for status polling (15s interval)
  - **Add query hook** to `apps/dashboard/hooks/queries.ts`:
    - `queryKeys.kakaoStatus: ["auth", "kakao", "status"]`
    - `useKakaoStatus()` hook fetching from `/api/auth/kakao/status` with 15s refetch interval
    - `useKakaoDisconnect()` mutation posting to `/api/auth/kakao/disconnect`
  - **Create API proxy routes** (matching existing proxy pattern):
    - `apps/dashboard/app/api/auth/kakao/status/route.ts` — GET proxy to `${BACKEND_URL}/auth/kakao/status`
    - `apps/dashboard/app/api/auth/kakao/disconnect/route.ts` — POST proxy to `${BACKEND_URL}/auth/kakao/disconnect`
    - `apps/dashboard/app/api/auth/kakao/start/route.ts` — GET redirect to `${BACKEND_URL}/auth/kakao/start` (or just use backend URL directly from frontend)
  - **Handle OAuth return**: Check for `?kakao=connected` query param on settings page load → show success toast/banner

  **Must NOT do**:
  - Do not create a general settings framework or preferences system
  - Do not add Discord/Telegram configuration to the settings page (they use env vars)
  - Do not add user authentication or login system
  - Do not use external UI libraries beyond what's already in the project (shadcn/ui, Tailwind, lucide)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Frontend page creation with UI components, responsive design, status indicators
  - **Skills**: [`senior-frontend`, `react-best-practices`]
    - `senior-frontend`: Next.js App Router patterns, TanStack Query integration, component organization
    - `react-best-practices`: Optimal data fetching, component composition, performance

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T11, T13)
  - **Parallel Group**: Wave 3
  - **Blocks**: F3 (QA needs frontend ready)
  - **Blocked By**: T8 (API contract — needs to know what status endpoint returns)

  **References**:

  **Pattern References**:
  - `apps/dashboard/components/sidebar.tsx:21-29` — `navItems` array with `{ href, label, icon }` objects. Add Settings entry in same format
  - `apps/dashboard/app/api/orders/route.ts` — API route proxy pattern: `BACKEND_URL` from env, `fetch()`, forward response with headers
  - `apps/dashboard/hooks/queries.ts` — `queryKeys` object, `fetchJSON<T>()` helper, `useQuery()` hooks with `refetchInterval`
  - `apps/dashboard/app/performance/page.tsx` — Page component pattern with card layout (for visual reference)
  - `apps/dashboard/components/query-provider.tsx` — QueryProvider wrapper (already in layout.tsx)

  **API/Type References**:
  - Backend `/auth/kakao/status` response: `{"connected": bool, "expires_at": string?, "refresh_expires_at": string?, "daily_sends": number?}`
  - Backend `/auth/kakao/disconnect` response: `{"ok": bool}`
  - Backend `/auth/kakao/start` — Redirects to Kakao (not a JSON API)

  **External References**:
  - `lucide-react` — `Settings` icon (already used: Activity, Layers, etc.)
  - `@tanstack/react-query` — `useMutation` for disconnect action (already in project)

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Settings page renders at /settings
    Tool: Playwright (skill: playwright)
    Preconditions: Frontend dev server running at localhost:3000
    Steps:
      1. Navigate to `http://localhost:3000/settings`
      2. Wait for page to load
      3. Assert page contains text "Settings"
      4. Assert page contains text "KakaoTalk" or "Kakao"
      5. Assert "Connect" button is visible (when not connected)
    Expected Result: Settings page renders with Kakao section
    Failure Indicators: 404, blank page, missing Kakao section
    Evidence: .sisyphus/evidence/task-12-settings-page.png

  Scenario: Settings nav item in sidebar
    Tool: Playwright
    Preconditions: Frontend running
    Steps:
      1. Navigate to `http://localhost:3000`
      2. Assert sidebar contains "Settings" link
      3. Click "Settings" link
      4. Assert URL changes to `/settings`
    Expected Result: Settings link in sidebar, navigates correctly
    Failure Indicators: Missing nav item, broken link
    Evidence: .sisyphus/evidence/task-12-sidebar.png

  Scenario: Frontend builds successfully
    Tool: Bash
    Preconditions: All frontend files created
    Steps:
      1. Run `cd apps/dashboard && npm run build`
      2. Assert exit code 0
    Expected Result: Build succeeds with no errors
    Failure Indicators: TypeScript errors, missing imports, build failure
    Evidence: .sisyphus/evidence/task-12-build.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Files: `apps/dashboard/components/sidebar.tsx`, `apps/dashboard/app/settings/page.tsx`, `apps/dashboard/hooks/queries.ts`, `apps/dashboard/app/api/auth/kakao/*/route.ts`

- [ ] 13. Backend Build + Regression Verification

  **What to do**:
  - Run full backend test suite: `cd backend && go test ./...` — ALL existing tests must pass
  - Run full backend build: `cd backend && go build -o /dev/null ./cmd/omo-core`
  - Run `go vet ./...` for static analysis
  - Run frontend build: `cd apps/dashboard && npm run build`
  - Verify no existing files were unintentionally modified:
    - `ports/notifier.go` unchanged
    - `notification/multi.go` unchanged
    - `app/notify/service.go` unchanged
  - Verify new Kakao tests pass in isolation: `go test ./internal/adapters/notification/ -run TestKakao -v`
  - Verify new handler tests pass: `go test ./internal/adapters/http/ -run TestKakao -v`

  **Must NOT do**:
  - Do not fix pre-existing test failures (report them)
  - Do not modify existing test files

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Running commands and checking outputs — no implementation
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (sequential — after T11, T12)
  - **Parallel Group**: Wave 3 (after T11, T12)
  - **Blocks**: F1-F4 (final verification)
  - **Blocked By**: T11 (wiring), T9 (adapter tests), T10 (handler tests)

  **References**:

  **Acceptance Criteria**:

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Full backend test suite passes
    Tool: Bash
    Preconditions: All implementation tasks complete
    Steps:
      1. Run `cd backend && go test ./... 2>&1`
      2. Assert no FAIL lines
      3. Count total tests run
    Expected Result: All tests PASS
    Failure Indicators: Any FAIL line in output
    Evidence: .sisyphus/evidence/task-13-all-tests.txt

  Scenario: Backend binary builds
    Tool: Bash
    Preconditions: All code compiles
    Steps:
      1. Run `cd backend && go build -o /dev/null ./cmd/omo-core`
      2. Assert exit code 0
    Expected Result: Clean build
    Failure Indicators: Compilation errors
    Evidence: .sisyphus/evidence/task-13-build.txt

  Scenario: Forbidden files unchanged
    Tool: Bash
    Preconditions: Implementation complete
    Steps:
      1. Run `git diff --name-only backend/internal/ports/notifier.go`
      2. Run `git diff --name-only backend/internal/adapters/notification/multi.go`
      3. Run `git diff --name-only backend/internal/app/notify/service.go`
      4. Assert all three return empty (no changes)
    Expected Result: No modifications to protected files
    Failure Indicators: Any of these files appear in git diff
    Evidence: .sisyphus/evidence/task-13-forbidden-check.txt

  Scenario: Frontend builds
    Tool: Bash
    Preconditions: All frontend files created
    Steps:
      1. Run `cd apps/dashboard && npm run build`
      2. Assert exit code 0
    Expected Result: Clean build
    Failure Indicators: TypeScript or build errors
    Evidence: .sisyphus/evidence/task-13-frontend-build.txt
  ```

  **Commit**: NO (verification only — no new code)

---

## Final Verification Wave

> 4 review agents run in PARALLEL. ALL must APPROVE. Rejection → fix → re-run.

- [ ] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in `.sisyphus/evidence/`. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] F2. **Code Quality Review** — `unspecified-high`
  Run `cd backend && go vet ./... && go build -o /dev/null ./cmd/omo-core && go test ./...`. Review all changed/new files for: `any` type abuse, empty error catches, `fmt.Println` in prod code, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic variable names (`data`/`result`/`item`/`temp`). Verify error wrapping uses `"kakao: "` prefix consistently.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [ ] F3. **Real Manual QA** — `unspecified-high` (+ `playwright` skill for UI)
  Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration: config present but no tokens → backend starts without Kakao → Settings page shows "Not Connected". Test edge cases: missing env vars, malformed callback code. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [ ] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (`git log`/`git diff`). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance: `ports/notifier.go` unmodified, `notification/multi.go` unmodified, `app/notify/service.go` unmodified, no general settings framework, no encryption, no rate limiting middleware. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Forbidden Files [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Wave | Commit | Message | Files | Pre-commit |
|------|--------|---------|-------|------------|
| 1 | T1-T5 grouped | `feat(notification): add kakao foundation (config, migration, ports, template, state)` | All Wave 1 files | `go build -o /dev/null ./cmd/omo-core` |
| 2 | T6-T10 grouped | `feat(notification): implement kakao adapter, token repo, oauth handler with tests` | All Wave 2 files | `go test ./internal/adapters/notification/ ./internal/adapters/timescaledb/ ./internal/adapters/http/` |
| 3 | T11-T13 grouped | `feat(notification): wire kakao notifier, add settings page` | main.go + frontend files | `go test ./... && cd apps/dashboard && npm run build` |

---

## Success Criteria

### Verification Commands
```bash
# Backend builds
cd backend && go build -o /dev/null ./cmd/omo-core  # Expected: exit 0

# All tests pass
cd backend && go test ./...  # Expected: PASS

# Frontend builds
cd apps/dashboard && npm run build  # Expected: exit 0

# Migration is idempotent
psql "$DATABASE_URL" -f migrations/015_create_kakao_tokens.up.sql  # Expected: CREATE TABLE
psql "$DATABASE_URL" -f migrations/015_create_kakao_tokens.up.sql  # Expected: no error

# Backend starts without Kakao config
cd backend && timeout 3 go run ./cmd/omo-core/ 2>&1 | grep -i kakao  # Expected: "not configured" or empty

# OAuth start endpoint returns redirect
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/auth/kakao/start  # Expected: 307

# Status endpoint returns disconnected
curl -s http://localhost:8080/auth/kakao/status  # Expected: {"connected": false}

# Settings page loads
curl -s -o /dev/null -w "%{http_code}" http://localhost:3000/settings  # Expected: 200
```

### Final Checklist
- [ ] All "Must Have" items present and verified
- [ ] All "Must NOT Have" items confirmed absent
- [ ] All tests pass (`go test ./...`)
- [ ] Frontend builds (`npm run build`)
- [ ] Existing Discord/Telegram notifications unaffected
- [ ] No modifications to `ports/notifier.go`, `notification/multi.go`, or `app/notify/service.go`
