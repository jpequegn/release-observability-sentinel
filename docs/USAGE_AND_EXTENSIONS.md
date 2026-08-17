# Usage and Extensions

## What It Demonstrates

The project demonstrates a controlled post-deploy observability loop:

1. Associate concrete release changes with affected services and evidence.
2. Generate failure hypotheses and only the queries needed to test them.
3. Apply cost, access, cadence, range, and data-sensitivity policy.
4. Require an attributed human approval before execution.
5. Recheck over time so delayed and intermittent regressions are visible.
6. Preserve redacted observations and verdicts in a verifiable receipt.
7. Escalate unhealthy, ambiguous, missing, and failed telemetry to the owner.

It is useful as a reference implementation, evaluation fixture, policy testbed,
and integration contract. It is not a production incident-response service.

## Typical Usage

### Review a Proposed Plan

```bash
go run ./cmd/sentinel plan preview --scenario scenario-003
```

Inspect the mapped services, hypotheses, evidence IDs, query bounds, cadence,
cost ceiling, owner, policy version, and `proposed` approval state.

### Evaluate the Strategy

```bash
go run ./cmd/sentinel replay \
  --json-report artifacts/replay.json \
  --markdown-report artifacts/replay.md
```

Use JSON for CI thresholds and Markdown for review. The command exits nonzero
if the adaptive strategy no longer beats the static baseline on the required
quality dimensions.

### Verify a Watch Receipt

```bash
go run ./cmd/sentinel ledger verify --path demo-output/delayed-release.jsonl
go run ./cmd/sentinel status --ledger demo-output/delayed-release.jsonl
```

Verification checks every event hash and previous-hash link before status is
reconstructed. Treat exit `1` as an operational attention signal and exit `2`
as an evidence-integrity or usage failure.

### Embed the Webhook Simulator

Construct `webhook.New` with a bearer token, replay window, clock, and ledger
directory, then mount it as an `http.Handler`. Send fictional release envelopes
to `POST /releases`; send `edit`, `approve`, or `cancel` actions to
`POST /plans/{id}/actions`. Every request needs:

- `Authorization: Bearer <token>`
- `X-Sentinel-Timestamp: <RFC3339>`
- `X-Sentinel-Nonce: <unique value>`
- `X-Idempotency-Key: <stable operation key>`

No live Slack or observability credentials are required.

## Practical Extensions

- Replace fixture adapters with read-only Prometheus HTTP API, Tempo search,
  and Loki query-range clients using per-tenant allowlists.
- Persist plans, approvals, nonce state, delivery state, and ledgers in a
  transactional store; use object-lock storage for externally immutable audit.
- Add workload identity, signed webhook bodies, key rotation, and per-owner
  authorization instead of a shared bearer token.
- Add a durable scheduler and outbox so checkpoints and notifications survive
  process restarts without duplicate execution.
- Learn thresholds from service SLO/error-budget policy while preserving a
  deterministic rule trace and abstention path.
- Add deployment-provider links and rollback recommendations while keeping all
  mutations behind a separate, explicit approval boundary.

## Related Project Integration

[Project #236, Production Context Freshness Service](https://github.com/jpequegn/project-ideas/issues/236)
can provide the typed temporal context packets already accepted by the mapper.
Its authority, validity interval, and freshness warnings should become policy
inputs; stale or future context must continue to fail closed.

[Project #227, Agent Trace Reliability Control Plane](https://github.com/jpequegn/project-ideas/issues/227)
can provide OTLP-derived agent reliability signals. A release touching an
agent workflow could then generate trace hypotheses for tool failures, retry
storms, handoff latency, or evaluation regressions and watch them through the
same approval and receipt path.

## Innovative Uses

- **Canary evidence contracts:** derive a watch plan before rollout and require
  its hypotheses, query budget, and success invariants as a deployment artifact.
- **Change-aware evaluation selection:** map prompt, model, tool, or policy diffs
  to only the agent eval suites and traces likely to regress.
- **Organizational dependency sensing:** compare declared service ownership and
  dependencies with observed release effects to identify stale catalog edges.
- **Counterfactual incident replay:** replay a historical release with alternate
  cadence or query budgets to estimate whether the regression would have been
  detected earlier and at what cost.
- **Evidence-backed progressive delivery:** feed verified read-only verdicts to
  a separate deployment controller as advisory evidence, never as implicit
  authority to promote or roll back.
- **Observability policy regression tests:** check proposed platform policy
  changes against the corpus to quantify coverage, noise, latency, and cost
  before applying them to production telemetry.
