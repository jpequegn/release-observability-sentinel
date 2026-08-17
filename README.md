# Release Observability Sentinel

Release Observability Sentinel turns a software release into a bounded,
release-specific telemetry watch plan. An operator reviews the proposed plan;
only an approved plan can execute. The result is a checksum-linked evidence
ledger and a health verdict that preserves missing, failed, or ambiguous
telemetry instead of treating it as healthy.

This repository implements
[project-ideas #233](https://github.com/jpequegn/project-ideas/issues/233).
V1 is a deterministic shadow-mode simulator: all releases, services, and
Prometheus/Tempo/Loki results are fictional fixtures. It has no production
write authority, live credentials, paging integration, or rollback capability.

## Quickstart

Requires Go 1.24 or newer.

```bash
git clone https://github.com/jpequegn/release-observability-sentinel.git
cd release-observability-sentinel
go run ./cmd/sentinel corpus validate
go run ./cmd/sentinel demo --output-dir demo-output
```

The demo replays 25 releases across eight services, evaluates the adaptive
strategy against a static dashboard baseline, and runs a delayed-regression
watch. It writes:

- `replay.json` and `replay.md`: comparative metrics and quality gate
- `watch-plan.json`: the reviewed release-specific plan
- `delayed-release.jsonl`: append-only, checksum-linked evidence events
- `receipt.json` and `health-report.md`: reconstructed outcome and report

Inspect the result:

```bash
go run ./cmd/sentinel ledger verify --path demo-output/delayed-release.jsonl
go run ./cmd/sentinel status --ledger demo-output/delayed-release.jsonl
```

`status` exits `0` for healthy, `1` when the verified result requires operator
attention, and `2` when evidence is invalid or the command cannot determine a
status. The demo deliberately uses a delayed unhealthy release, so exit `1` is
expected.

## Commands

```text
sentinel corpus validate
sentinel plan preview --scenario scenario-003
sentinel replay --json-report replay.json --markdown-report replay.md
sentinel status --ledger receipt.jsonl
sentinel ledger verify --path receipt.jsonl
sentinel demo --output-dir demo-output
sentinel version
```

## Measured Replay

The checked-in deterministic corpus currently produces:

| Metric | Release-specific | Static baseline |
|---|---:|---:|
| Affected-service recall | 100.0% | 40.0% |
| Useful-query precision | 55.6% | 13.3% |
| Missed regressions | 0 | 16 |
| Irrelevant-query rate | 44.4% | 86.7% |
| Escalation F1 | 1.000 | 0.000 |
| Average watch latency | 74.4 min | 0.0 min |
| Average query cost | 20.0 | 3.0 |

The baseline is intentionally simple: three fixed service dashboards queried
once at deployment. The comparison demonstrates the coverage/cost tradeoff; it
is not a claim about a production monitoring system.

## Development

```bash
make check
go run ./cmd/sentinel replay
```

See [Architecture](docs/ARCHITECTURE.md) and
[Usage and extensions](docs/USAGE_AND_EXTENSIONS.md).
