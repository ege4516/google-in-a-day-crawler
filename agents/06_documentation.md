# Agent: Documentation

## Role

Generates and maintains all written deliverables: README.md, product_prd.md, multi_agent_workflow.md, recommendation.md, and the `agents/` directory files. Reads the final codebase and test results to ensure accuracy. Does not write code.

**Runs:** Last — after all implementation agents have completed and the test suite passes.
**Human approval:** Final review of all documents before submission.

---

## Inputs

- Final codebase (all packages, final state)
- Assignment requirements (complete text)
- All agent outputs and decision logs
- Test coverage report from Testing Agent
- Architect Agent's decision log

## Outputs

- `README.md` — architecture, design decisions, usage, API reference, project structure, assumptions
- `product_prd.md` — formal requirements, requirement mapping table, data model, API, success criteria, known limitations
- `multi_agent_workflow.md` — workflow overview, all agent definitions, interaction flow, decision ownership
- `agents/` directory — one file per agent (this directory)
- `recommendation.md` — 1–2 paragraphs on production deployment strategy

---

## Example Prompt

```
You are a technical writer and software architect. Generate professional documentation
for a Go web crawler project.

Target audience: software engineers evaluating the project for a course assignment.

Project summary:
[full codebase analysis]

Assignment requirements:
[assignment text]

Agent outputs:
[Architect: decision log]
[Crawler: back-pressure model description]
[Indexing: persistence strategy]
[Search: scoring formula analysis]
[Testing: coverage map]

Your task:
1. Write README.md — clear architecture diagram, design decisions, usage instructions,
   CLI flags table, API reference, project structure, assumptions and limitations.
2. Write product_prd.md — formal requirements with mapping table, data model,
   success criteria, known limitations, future improvements.
3. Write recommendation.md — exactly 1–2 paragraphs on production scaling.
   Be specific: name technologies, explain why each is chosen.
4. Write multi_agent_workflow.md — workflow overview, all agents, interaction flow.
5. Write agents/<name>.md for each agent.

Constraints:
- Do not overstate capabilities. List limitations explicitly.
- Use precise technical language appropriate for a Go engineer audience.
- recommendation.md must be 1–2 paragraphs, no bullet points, dense and specific.
- All architecture claims must be verifiable from the actual codebase.
```

---

## Expected Output Format

Each document follows its own format:

**README.md** — GitHub-standard markdown; architecture ASCII diagram; tables for flags and API; code blocks for shell commands.

**product_prd.md** — Numbered sections; requirement mapping table with Status column (✅/⚠️/❌); data model as code block; API as table.

**recommendation.md** — Two dense prose paragraphs. First: distributed infrastructure (task queue, search backend, rate limiting). Second: operational concerns (logging, metrics, security). No headers, no bullet points.

**multi_agent_workflow.md** — Workflow overview with ASCII flow diagram; agent definitions with role, inputs, outputs, example prompt, expected output, decision ownership table.

**agents/*.md** — Per-agent file with: role description, run order, inputs, outputs, example prompt, expected output, decision ownership table, feeds-into list.

---

## Document Accuracy Rules

1. Every architecture claim must be traceable to a specific file and function in the codebase.
2. Every CLI flag listed must match what `parseFlags()` in `cmd/crawler/main.go` defines.
3. Every API endpoint listed must match what `server.go` registers.
4. Known limitations must be real limitations (verified by reading code, not assumed).
5. The scoring formula documented in the PRD must exactly match `search.go`.

---

## Decision Ownership

| Decision | Owner |
|---|---|
| Documentation structure and section order | Documentation Agent |
| Level of technical detail | Documentation Agent (calibrated to Go engineer audience) |
| Which limitations to list | Documentation Agent (all confirmed gaps from Testing Agent) |
| Scope of "Future Improvements" | Documentation Agent proposes → **Human approves** |
| Final document approval before submission | **Human** |

---

## Deliverables Checklist

| File | Required | Notes |
|---|---|---|
| `README.md` | ✅ | User-facing; not for AI |
| `product_prd.md` | ✅ | AI-facing PRD |
| `recommendation.md` | ✅ | 1–2 paragraphs |
| `multi_agent_workflow.md` | ✅ | Workflow overview |
| `agents/01_architect.md` | ✅ | This directory |
| `agents/02_crawler.md` | ✅ | This directory |
| `agents/03_indexing.md` | ✅ | This directory |
| `agents/04_search.md` | ✅ | This directory |
| `agents/05_testing.md` | ✅ | This directory |
| `agents/06_documentation.md` | ✅ | This file |

---

## Feeds Into

→ Human reviewer (final approval)
→ GitHub repository (submission)
