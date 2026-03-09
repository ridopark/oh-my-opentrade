# Draft: KakaoTalk "Send to Me" Notification Adapter

## Requirements (confirmed)
- Add KakaoTalk Memo API as 3rd notification adapter alongside Discord/Telegram
- Implement `NotifierPort` interface as-is (no interface changes)
- OAuth2 flow: authorize → token exchange → token refresh cycle
- Stateful adapter: must manage access/refresh token lifecycle
- Token storage via a NEW port abstraction (not leak DB into adapter)
- Follow hexagonal architecture strictly
- Config: YAML + env overlay pattern (matching existing)
- Testing: match existing patterns (httptest.NewServer, external test package)

## Technical Decisions
- KakaoTalk API endpoint: `POST https://kapi.kakao.com/v2/api/talk/memo/default/send`
- Auth: `Authorization: Bearer {access_token}` header
- Body: form-encoded `template_object` JSON (text template type)
- Access token TTL: ~6-12 hours → auto-refresh needed
- Refresh token TTL: ~2 months → re-auth via dashboard when expired

## Key Architectural Difference from Discord/Telegram
- Discord/Telegram: stateless, static credentials, fire-and-forget
- KakaoTalk: stateful, OAuth2 tokens, needs persistence + auto-refresh
- New port needed: `TokenStorePort` for reading/writing/refreshing tokens
- Adapter wraps: token store + HTTP client + OAuth2 refresh logic

## Research Findings
- Existing adapters follow clear pattern: constructor takes credentials + http.Client
- MultiNotifier fan-out handles multiple notifiers, joins errors
- Main.go wiring: conditional on config fields being non-empty
- Notify service is domain-event driven, non-fatal notification failures

## Open Questions
- Token storage backend: TimescaleDB (existing) or file-based?
- Dashboard UI scope: connect/disconnect only, or also test message?
- Cross-channel notification: alert via Discord/Telegram when KakaoTalk needs re-auth?
- OAuth callback: Go backend HTTP endpoint or Next.js API route?

## Scope Boundaries
- INCLUDE: Go adapter, token store port+adapter, config, wiring, tests, dashboard OAuth UI
- EXCLUDE: Changes to NotifierPort interface, changes to existing adapters
