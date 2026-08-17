package watch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
	"github.com/jpequegn/release-observability-sentinel/internal/ledger"
	"github.com/jpequegn/release-observability-sentinel/internal/telemetry"
)

func runnerFor(scenario corpus.Scenario, plan domain.WatchPlan, path string) (Runner, *VirtualClock) {
	adapters := map[domain.Backend]telemetry.Adapter{}
	for _, backend := range []domain.Backend{domain.BackendPrometheus, domain.BackendTempo, domain.BackendLoki} {
		adapters[backend] = telemetry.FixtureAdapter{Kind: backend, Scenario: scenario}
	}
	clock := NewVirtualClock(scenario.Release.DeployedAt)
	return Runner{Executor: telemetry.Executor{Adapters: adapters}, Clock: clock, Ledger: &ledger.Ledger{Path: path}}, clock
}

func fixturePlan(scenario corpus.Scenario) domain.WatchPlan {
	queries := []domain.QuerySpec{}
	for _, backend := range []domain.Backend{domain.BackendPrometheus, domain.BackendTempo, domain.BackendLoki} {
		queries = append(queries, domain.QuerySpec{ID: string(backend), Backend: backend, Range: 15 * time.Minute, Deadline: time.Second, ResultLimit: 10})
	}
	return domain.WatchPlan{ID: "plan-1", ReleaseID: scenario.Release.ID, Owner: scenario.Release.Owner, Queries: queries, CheckOffsets: []time.Duration{0, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour}, MaxDuration: 3 * time.Hour, Approval: domain.Approval{State: domain.ApprovalApproved, Actor: "operator"}}
}

func scenarioByClass(t *testing.T, class string) corpus.Scenario {
	t.Helper()
	for _, scenario := range corpus.Load().Scenarios {
		if scenario.Class == class {
			return scenario
		}
	}
	t.Fatalf("missing scenario class %s", class)
	return corpus.Scenario{}
}

func TestRunnerRejectsUnapprovedPlan(t *testing.T) {
	scenario := scenarioByClass(t, "normal")
	plan := fixturePlan(scenario)
	plan.Approval.State = domain.ApprovalProposed
	runner, _ := runnerFor(scenario, plan, filepath.Join(t.TempDir(), "ledger.jsonl"))
	if _, err := runner.Run(context.Background(), scenario.Release, plan); err == nil || !strings.Contains(err.Error(), "approved") {
		t.Fatalf("expected approval error, got %v", err)
	}
}

func TestRunnerDetectsDelayedRegressionAndReconstructsReceipt(t *testing.T) {
	scenario := scenarioByClass(t, "delayed")
	plan := fixturePlan(scenario)
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	runner, clock := runnerFor(scenario, plan, path)
	receipt, err := runner.Run(context.Background(), scenario.Release, plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Verdict.State != domain.HealthUnhealthy || len(clock.Waits()) != 4 {
		t.Fatalf("verdict=%s waits=%d", receipt.Verdict.State, len(clock.Waits()))
	}
	rebuilt, err := ledger.Reconstruct(path)
	if err != nil || rebuilt.Verdict.State != receipt.Verdict.State || len(rebuilt.Checks) != len(receipt.Checks) {
		t.Fatalf("rebuilt=%#v err=%v", rebuilt, err)
	}
	if !strings.Contains(RenderReport(receipt), "Escalation required") {
		t.Fatal("report should route an escalation")
	}
}

func TestRunnerKeepsUnknownStatesNonHealthy(t *testing.T) {
	for _, class := range []string{"missing", "query_failure"} {
		t.Run(class, func(t *testing.T) {
			scenario := scenarioByClass(t, class)
			plan := fixturePlan(scenario)
			runner, _ := runnerFor(scenario, plan, filepath.Join(t.TempDir(), "ledger.jsonl"))
			receipt, err := runner.Run(context.Background(), scenario.Release, plan)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Verdict.State == domain.HealthHealthy || !receipt.Verdict.Escalate {
				t.Fatalf("verdict=%#v", receipt.Verdict)
			}
		})
	}
}

func TestIntermittentRegressionIsAmbiguous(t *testing.T) {
	scenario := scenarioByClass(t, "intermittent")
	plan := fixturePlan(scenario)
	runner, _ := runnerFor(scenario, plan, filepath.Join(t.TempDir(), "ledger.jsonl"))
	receipt, err := runner.Run(context.Background(), scenario.Release, plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Verdict.State != domain.HealthAmbiguous {
		t.Fatalf("verdict=%#v", receipt.Verdict)
	}
}

func TestHealthyCheckpointEventsAreSuppressed(t *testing.T) {
	scenario := scenarioByClass(t, "normal")
	plan := fixturePlan(scenario)
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	runner, _ := runnerFor(scenario, plan, path)
	if _, err := runner.Run(context.Background(), scenario.Release, plan); err != nil {
		t.Fatal(err)
	}
	events, err := ledger.Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	checkpointEvents := 0
	for _, event := range events {
		if event.Type == "checkpoint_verdict" {
			checkpointEvents++
		}
	}
	if checkpointEvents != 1 {
		t.Fatalf("checkpoint events=%d want=1", checkpointEvents)
	}
}
