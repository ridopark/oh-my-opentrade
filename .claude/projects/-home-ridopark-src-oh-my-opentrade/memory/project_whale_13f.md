---
name: 13F whale accumulation pipeline
description: SEC EDGAR 13F filing ingest, CUSIP resolution, whale accumulation scoring, and strategy confluence integration
type: project
---

13F whale accumulation pipeline built 2026-04-09.

**Why:** Identify institutional accumulation patterns to use as a confluence factor alongside dark pool data and box range breakouts.

**How to apply:**
- CLI backfill: `go run ./cmd/omo-backfill-13f --from-quarter 2023Q1 --user-agent "Company email@example.com"`
- Scheduled refresh: runs daily at 6 AM ET in omo-core when `SEC_USER_AGENT` env var is set
- Confluence: `WhaleScore` field on `IndicatorData`, consumed in avwap `computeConfluence()` as Factor 6 (+2/+3 pts)
- Key packages: `adapters/sec/`, `adapters/openfigi/`, `adapters/timescaledb/whale_repo.go`, `app/whale13f/`
- Migration: `027_create_whale_tables` (whale_filings, whale_accumulation, cusip_ticker_cache)
- Filer CIKs in `sec/filer_config.go` — verify against live EDGAR before production use
