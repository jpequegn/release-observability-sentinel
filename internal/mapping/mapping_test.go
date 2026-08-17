package mapping

import (
	"context"
	"testing"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

func TestMapsChangesAndExpandsDownstreamDependencies(t *testing.T) {
	data := corpus.Load()
	release := data.Scenarios[1].Release
	result, err := New(data.Services, nil).Map(context.Background(), release, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, mapped := range result.Services {
		ids[mapped.Service.ID] = true
		if len(mapped.ReleaseEvidence) == 0 {
			t.Fatalf("%s missing release evidence", mapped.Service.ID)
		}
	}
	if !ids["currency"] || !ids["checkout"] {
		t.Fatalf("mapped services = %#v", ids)
	}
}

func TestUnknownPathsRemainUnknown(t *testing.T) {
	data := corpus.Load()
	release := data.Scenarios[0].Release
	release.Changed = append(release.Changed, domain.ChangedItem{Path: "misc/unknown.txt", Kind: domain.ChangeApplication, EvidenceID: release.Evidence[0].ID})
	result, err := New(data.Services, nil).Map(context.Background(), release, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Unknown) != 1 || result.Unknown[0].Path != "misc/unknown.txt" {
		t.Fatalf("unknown = %#v", result.Unknown)
	}
}

func TestTemporalContextRejectsFutureFactsAndAcceptsCurrentFacts(t *testing.T) {
	data := corpus.Load()
	release := data.Scenarios[0].Release
	service := data.Services[0]
	future := release.DeployedAt.Add(time.Hour)
	packet := ContextPacket{Service: service.ID, QuestionTime: release.DeployedAt, State: "current", Facts: []ContextFact{{FactID: "fact-1", Service: service.ID, Predicate: "uses_metric", Value: "new_metric", State: "current", RecordedAt: future, ValidFrom: release.DeployedAt, Source: service.Evidence}}}
	mapper := New(data.Services, nil)
	result, err := mapper.Map(context.Background(), release, []ContextPacket{packet})
	if err != nil {
		t.Fatal(err)
	}
	if contains(result.Services[0].Signals[domain.BackendPrometheus], "new_metric") {
		t.Fatal("future metric leaked into mapping")
	}
	packet.Facts[0].RecordedAt = release.DeployedAt.Add(-time.Minute)
	result, _ = mapper.Map(context.Background(), release, []ContextPacket{packet})
	if !contains(result.Services[0].Signals[domain.BackendPrometheus], "new_metric") {
		t.Fatal("current metric was not added")
	}
}

func TestQuestionTimeAfterReleaseIsIgnored(t *testing.T) {
	data := corpus.Load()
	release := data.Scenarios[0].Release
	packet := ContextPacket{Service: "checkout", QuestionTime: release.DeployedAt.Add(time.Minute), State: "current"}
	result, err := New(data.Services, nil).Map(context.Background(), release, []ContextPacket{packet})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Services[0].MissingContext) == 0 {
		t.Fatal("future context packet should be ignored")
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
