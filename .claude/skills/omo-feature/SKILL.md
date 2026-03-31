---
name: omo-feature
description: "oh-my-opentrade full-stack feature development orchestrator. Use this skill when implementing new features that span both the Go backend and Next.js frontend, or when changes are needed across multiple layers. Triggers on 'new feature', 'full-stack', 'end-to-end', 'build feature', 'add feature' keywords. Does NOT trigger for single-layer work (backend-only or frontend-only)."
---

# OMO Feature Orchestrator

Orchestrates full-stack feature development for oh-my-opentrade via agent collaboration.

## Execution Mode: Sub-agent

## Agent Configuration

| Agent | subagent_type | Role | Skill | Output |
|-------|--------------|------|-------|--------|
| go-architect | go-architect | Backend implementation (domain > ports > adapters > service > handler) | go-hexagonal | Go source + tests + migrations |
| dashboard-dev | dashboard-dev | Frontend implementation (pages > components > hooks) | senior-frontend | React components + pages + hooks |
| qa-inspector | qa-inspector | Integration coherence verification | (inline) | Verification report |

## Workflow

### Phase 1: Requirements Analysis

1. Analyze the user's feature request
2. Determine scope of impact:
   - **Domain changes**: new entities, value objects, event types needed?
   - **Port changes**: new interfaces or extensions to existing ones?
   - **DB changes**: new tables/columns, migrations needed?
   - **API changes**: new endpoints or modifications to existing ones?
   - **Frontend changes**: new pages/components/hooks?
3. Create `_workspace/` in the working directory

### Phase 2: Backend Implementation

**Execution**: Sequential (backend API is a prerequisite for frontend)

Invoke go-architect sub-agent:

```
Agent(
  prompt: "Implement the following feature in the oh-my-opentrade backend: {feature description}
    Requirements: {detailed requirements}
    Read the go-hexagonal skill and follow its architecture patterns.
    Implementation order: domain > ports > adapters > app > http handler > wiring (cmd/omo-core/)
    Write tests and verify with `cd backend && go build ./...` + `go test ./...`.
    When done, record the list of changed files and new API endpoint specs in _workspace/01_backend_spec.md.",
  subagent_type: "go-architect",
  model: "opus"
)
```

Output: source code + `_workspace/01_backend_spec.md`

### Phase 3: Frontend Implementation

**Execution**: Sequential, after Phase 2 completes

Invoke dashboard-dev sub-agent:

```
Agent(
  prompt: "Implement the following feature in the oh-my-opentrade dashboard: {feature description}
    Backend API spec: Read _workspace/01_backend_spec.md to check API response shapes.
    Implementation order: type definitions > API hooks > components > pages > routing
    Backend JSON tag field names must exactly match TypeScript types.
    When done, record the list of changed files in _workspace/02_frontend_spec.md.",
  subagent_type: "dashboard-dev",
  model: "opus"
)
```

Output: React code + `_workspace/02_frontend_spec.md`

### Phase 4: Integration Verification

**Execution**: Sequential, after Phase 3 completes

Invoke qa-inspector sub-agent:

```
Agent(
  prompt: "Perform integration coherence verification for the following oh-my-opentrade feature: {feature description}
    Change scope:
    - Backend: see _workspace/01_backend_spec.md
    - Frontend: see _workspace/02_frontend_spec.md
    Verify:
    1. Go HTTP handler JSON response shapes match frontend TypeScript types
    2. DB migration columns match domain entity fields match Repository SQL
    3. Event type publishing matches subscription handler completeness
    4. Frontend route paths match actual page files
    Record results in _workspace/03_qa_report.md.",
  subagent_type: "qa-inspector",
  model: "opus"
)
```

Output: `_workspace/03_qa_report.md`

### Phase 5: Fix and Finalize

1. If the QA report has FAIL items:
   - Backend issues — re-invoke go-architect to fix
   - Frontend issues — re-invoke dashboard-dev to fix
   - Maximum 1 re-verification loop
2. Build verification:
   ```bash
   cd backend && go build ./... && go test ./...
   cd apps/dashboard && npm run build
   ```
3. Preserve `_workspace/` (for post-hoc verification)
4. Report summary to user

## Data Flow

```
User Request
    |
[Phase 1: Analysis] -> Requirements breakdown
    |
[Phase 2: go-architect] -> Backend code + API spec
    |
[Phase 3: dashboard-dev] -> Frontend code (references API spec)
    |
[Phase 4: qa-inspector] -> Verification report
    |
[Phase 5: Fix] -> Build check -> Done
```

## Error Handling

| Situation | Strategy |
|-----------|----------|
| go-architect build failure | Retry once with error message. On second failure, report to user |
| dashboard-dev build failure | Analyze TypeScript errors, retry once |
| qa-inspector finds FAILs | Re-invoke the responsible agent to fix (max 1 time) |
| Majority of agents fail | Notify user and ask whether to proceed |

## Test Scenarios

### Happy Path
1. User requests "new API endpoint + dashboard page"
2. Phase 1 identifies domain/port/adapter/handler/frontend change scope
3. Phase 2: go-architect implements backend + tests pass
4. Phase 3: dashboard-dev implements frontend (based on API spec)
5. Phase 4: qa-inspector reports all PASS
6. Phase 5: build confirmed, done

### Error Path
1. Phase 4: qa-inspector reports "JSON field name mismatch" FAIL
2. Phase 5: re-invoke dashboard-dev to fix types
3. Build re-verified, done
4. QA report updated with "1 issue fixed"
