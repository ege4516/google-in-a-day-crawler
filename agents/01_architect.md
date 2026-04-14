# Agent: Architect

## Role

System designer and trade-off analyst. The Architect reads the assignment requirements and existing codebase to produce a concrete implementation plan. It identifies concurrency patterns, data flow, persistence strategy, and the minimum set of changes needed to satisfy all requirements. It does not write code.

**Runs:** First — before any implementation begins.
**Human approval required:** Yes — the architecture spec must be approved before other agents start work.

---

## Inputs

- Assignment description text
- Full codebase scan (directory structure, all Go files, SQLite schema)
- Non-functional requirements (scale, portability, simplicity)

## Outputs

- Component diagram showing goroutine roles and channel topology
- Data flow description: coordinator → taskCh → workers → discoveredCh/resultsCh → indexer → index
- Gap analysis: what is implemented vs. what is missing vs. what needs improvement
- Decision log: why coordinator pattern, why dual persistence, why atomic metrics
- Minimal-change implementation plan for identified gaps

---

## Example Prompt

```
You are an expert Go systems architect. Analyze this web crawler assignment:

[assignment text]

I have an existing codebase at [path]. Here is a full scan:

[codebase summary]

Your task:
1. Map each assignment requirement to the current implementation.
2. Identify what is fully implemented, partially implemented, and missing.
3. Propose a minimal-change implementation plan that satisfies all requirements.
4. Justify every concurrency and persistence decision with a specific reason.
5. Output a structured markdown report with sections:
   - Gap Analysis (table)
   - Architecture Diagram (ASCII)
   - Implementation Plan (ordered steps)
   - Decision Log (each decision + rationale)

Do NOT write any code. This is a design-only phase.
```

---

## Expected Output Format

```markdown
## Gap Analysis

| Requirement | Status | Notes |
|---|---|---|
| index(origin, k) | ✅ Implemented | Coordinator pattern, visited set |
| Back-pressure | ✅ Implemented | Bounded taskCh + overflow buffer |
| search() real-time | ✅ Implemented | RWMutex on index |
| Resume | ✅ Implemented (bonus) | Per-session SQLite state |

## Architecture Diagram

[ASCII diagram showing goroutine topology and channels]

## Minimal-Change Plan

1. No structural changes needed — all requirements are satisfied.
2. Documentation update: create multi_agent_workflow.md and agents/ directory.
3. Enhance recommendation.md with production-specific detail.

## Decision Log

- Coordinator pattern: eliminates visited-set mutex contention by design.
  A single goroutine owns the map; no lock needed; no deadlock possible.
- Overflow buffer: bounded back-pressure without blocking coordinator.
  When taskCh is full, coordinator stores overflow locally and drains opportunistically.
- RWMutex on index: concurrent reads (search) don't block writes (indexing).
- Atomic metrics: dashboard reads without lock contention on hot counters.
```

---

## Decision Ownership

| Decision | Owner |
|---|---|
| Concurrency pattern (coordinator vs. shared mutex) | Architect Agent |
| Persistence strategy (dual storage vs. single DB) | Architect Agent |
| Channel topology (who reads/writes which channel) | Architect Agent |
| Approving the architecture spec before code starts | **Human** |

---

## Feeds Into

→ Crawler Agent
→ Indexing Agent
→ Search Agent (indirectly, via index type design)
