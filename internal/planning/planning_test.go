package planning

import (
	"context"
	"testing"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
	"github.com/jpequegn/release-observability-sentinel/internal/mapping"
)

func mappedRelease(t *testing.T, withContext bool) (domain.ReleaseEnvelope, mapping.Result) {
	t.Helper()
	data := corpus.Load()
	release := data.Scenarios[0].Release
	packets := []mapping.ContextPacket{}
	if withContext {
		for _, service := range data.Services {
			packets = append(packets, mapping.ContextPacket{Service: service.ID, QuestionTime: release.DeployedAt, State: "current", Facts: []mapping.ContextFact{{FactID: "fact-" + service.ID, Service: service.ID, Predicate: "uses_metric", Value: service.Signals[domain.BackendPrometheus][0], State: "current", RecordedAt: release.DeployedAt.Add(-time.Minute), ValidFrom: release.DeployedAt.Add(-time.Hour), Source: service.Evidence}}})
		}
	}
	result, err := mapping.New(data.Services, nil).Map(context.Background(), release, packets)
	if err != nil {
		t.Fatal(err)
	}
	return release, result
}

func TestBuildsDeterministicEvidenceLinkedPlan(t *testing.T) {
	release, mapped := mappedRelease(t, true)
	planner := New(DefaultLimits())
	first, decision, err := planner.Build(release, mapped)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := planner.Build(release, mapped)
	if !decision.Allowed || first.ID != second.ID || len(first.Queries) == 0 {
		t.Fatalf("allowed=%v first=%s second=%s queries=%d", decision.Allowed, first.ID, second.ID, len(first.Queries))
	}
	for _, query := range first.Queries {
		if query.HypothesisID == "" || len(query.EvidenceIDs) == 0 {
			t.Fatalf("query missing evidence links: %#v", query)
		}
	}
}

func TestBuildPrioritizesDirectServiceAndCapsExpandedQueries(t *testing.T) {
	data := corpus.Load()
	scenario := data.Scenarios[5]
	packets := []mapping.ContextPacket{}
	for _, service := range data.Services {
		packets = append(packets, mapping.ContextPacket{Service: service.ID, QuestionTime: scenario.Release.DeployedAt, State: "current", Facts: []mapping.ContextFact{{FactID: "fact-" + service.ID, Service: service.ID, Predicate: "uses_metric", Value: service.Signals[domain.BackendPrometheus][0], State: "current", RecordedAt: scenario.Release.DeployedAt.Add(-time.Minute), ValidFrom: service.RecordedAt, Source: service.Evidence}}})
	}
	mapped, err := mapping.New(data.Services, nil).Map(context.Background(), scenario.Release, packets)
	if err != nil {
		t.Fatal(err)
	}
	plan, decision, err := New(DefaultLimits()).Build(scenario.Release, mapped)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
	if len(plan.Queries) != DefaultLimits().MaxQueries || plan.Queries[0].Service != "notifications" {
		t.Fatalf("queries=%d first=%s", len(plan.Queries), plan.Queries[0].Service)
	}
}

func TestMissingContextFailsClosed(t *testing.T) {
	release, mapped := mappedRelease(t, false)
	plan, decision, err := New(DefaultLimits()).Build(release, mapped)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || plan.Approval.State != domain.ApprovalAbstained {
		t.Fatalf("decision=%#v approval=%s", decision, plan.Approval.State)
	}
}

func TestPolicyRejectsUnsafeQueriesAndBudgets(t *testing.T) {
	release, mapped := mappedRelease(t, true)
	plan, _, _ := New(DefaultLimits()).Build(release, mapped)
	plan.Queries[0].Expression = "delete from alerts token=secret"
	plan.Queries[0].Range = 24 * time.Hour
	plan.Queries[0].Backend = domain.Backend("unknown")
	decision := Evaluate(plan, DefaultLimits())
	codes := map[string]bool{}
	for _, denial := range decision.Denials {
		codes[denial.Code] = true
	}
	for _, wanted := range []string{"backend", "query_bounds", "write_query", "sensitive_field"} {
		if !codes[wanted] {
			t.Fatalf("missing denial %s in %#v", wanted, decision.Denials)
		}
	}
}

func TestApprovalTransitionsRequirePassingPolicy(t *testing.T) {
	release, mapped := mappedRelease(t, true)
	plan, _, _ := New(DefaultLimits()).Build(release, mapped)
	approved, err := Transition(plan, domain.ApprovalApproved, "operator", "looks bounded", release.DeployedAt.Add(time.Minute), DefaultLimits())
	if err != nil || approved.Approval.State != domain.ApprovalApproved || approved.Approval.Version != 2 {
		t.Fatalf("approved=%#v err=%v", approved.Approval, err)
	}
	if _, err := Transition(approved, domain.ApprovalCancelled, "operator", "stop", release.DeployedAt.Add(2*time.Minute), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	plan.Queries[0].ResultLimit = 0
	if _, err := Transition(plan, domain.ApprovalApproved, "operator", "unsafe", release.DeployedAt.Add(time.Minute), DefaultLimits()); err == nil {
		t.Fatal("expected policy-blocked approval")
	}
}
