package telemetry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

func query(backend domain.Backend) domain.QuerySpec {
	return domain.QuerySpec{ID: "query-1", Backend: backend, Expression: "signal", Service: "checkout", HypothesisID: "hyp-1", EvidenceIDs: []string{"ev-1"}, Range: 15 * time.Minute, Deadline: 50 * time.Millisecond, ResultLimit: 10, EstimatedCost: 1}
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

func TestFixtureAdaptersAndGraderCoverHealthStates(t *testing.T) {
	tests := []struct {
		class string
		want  domain.HealthState
	}{
		{"normal", domain.HealthHealthy},
		{"immediate", domain.HealthUnhealthy},
		{"ambiguous", domain.HealthAmbiguous},
		{"missing", domain.HealthMissingTelemetry},
		{"query_failure", domain.HealthQueryFailure},
	}
	for _, test := range tests {
		t.Run(test.class, func(t *testing.T) {
			scenario := scenarioByClass(t, test.class)
			adapter := FixtureAdapter{Kind: domain.BackendPrometheus, Scenario: scenario}
			executor := Executor{Adapters: map[domain.Backend]Adapter{domain.BackendPrometheus: adapter}, Now: func() time.Time { return scenario.Release.DeployedAt }}
			checkAt := scenario.Release.DeployedAt.Add(time.Minute)
			if scenario.FailureStart > 0 {
				checkAt = scenario.Release.DeployedAt.Add(scenario.FailureStart)
			}
			observation := executor.Execute(context.Background(), query(domain.BackendPrometheus), checkAt)
			verdict := Grade([]domain.Observation{observation}, checkAt)
			if verdict.State != test.want {
				t.Fatalf("state=%s want=%s observation=%#v", verdict.State, test.want, observation)
			}
		})
	}
}

func TestLokiRedactsBeforeDigestAndCapsResults(t *testing.T) {
	scenario := scenarioByClass(t, "immediate")
	adapter := FixtureAdapter{Kind: domain.BackendLoki, Scenario: scenario}
	spec := query(domain.BackendLoki)
	spec.ResultLimit = 1
	executor := Executor{Adapters: map[domain.Backend]Adapter{domain.BackendLoki: adapter}}
	observation := executor.Execute(context.Background(), spec, scenario.Release.DeployedAt.Add(time.Minute))
	joined := strings.Join(observation.Messages, " ")
	if !observation.Redacted || strings.Contains(joined, "Bearer-fixture") || strings.Contains(joined, "fictional@example") {
		t.Fatalf("unredacted observation: %#v", observation)
	}
	if observation.RawDigest == "" {
		t.Fatal("redacted raw result must be digested")
	}
}

func TestExecutorHonorsDeadline(t *testing.T) {
	scenario := scenarioByClass(t, "normal")
	adapter := FixtureAdapter{Kind: domain.BackendPrometheus, Scenario: scenario, Delay: 100 * time.Millisecond}
	spec := query(domain.BackendPrometheus)
	spec.Deadline = time.Millisecond
	executor := Executor{Adapters: map[domain.Backend]Adapter{domain.BackendPrometheus: adapter}}
	observation := executor.Execute(context.Background(), spec, scenario.Release.DeployedAt)
	if !strings.Contains(observation.Error, "deadline") {
		t.Fatalf("error=%q", observation.Error)
	}
}

func TestConflictingObservationsAreAmbiguous(t *testing.T) {
	now := time.Now().UTC()
	observations := []domain.Observation{
		{ID: "healthy", Values: map[string]float64{"health_ratio": 1}},
		{ID: "bad", Values: map[string]float64{"health_ratio": 0.8}},
	}
	verdict := Grade(observations, now)
	if verdict.State != domain.HealthAmbiguous || !verdict.Escalate {
		t.Fatalf("verdict=%#v", verdict)
	}
}

func TestNoObservationsIsInsufficientEvidence(t *testing.T) {
	verdict := Grade(nil, time.Now().UTC())
	if verdict.State != domain.HealthInsufficientEvidence {
		t.Fatalf("state=%s", verdict.State)
	}
}
