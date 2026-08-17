# Release Observability Sentinel

A shadow-mode Go service that turns software releases into bounded telemetry
watch plans and reconstructable evidence-backed health reports.

This repository implements
[project-ideas #233](https://github.com/jpequegn/project-ideas/issues/233).
V1 uses fictional release and telemetry fixtures only. It cannot write to
production, change alerts, page operators, or trigger rollback.

## Development

```bash
go run ./cmd/sentinel version
go test ./...
go vet ./...
```

The implementation is organized through the repository issue tracker.
Shadow-mode release-specific telemetry watch plans with replayable evidence receipts
