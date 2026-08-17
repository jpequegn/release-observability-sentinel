package evaluation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

func TestReplayPassesComparativeQualityGate(t *testing.T) {
	report, err := Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.ReleaseCount < 25 || report.ServiceCount != 8 || !report.Gate.Passed {
		t.Fatalf("report=%#v", report)
	}
	if report.Adaptive.MissedRegressions != 0 || report.Adaptive.EscalationF1 != 1 {
		t.Fatalf("adaptive=%#v", report.Adaptive)
	}
	if report.Adaptive.AffectedServiceRecall <= report.Static.AffectedServiceRecall || report.Adaptive.IrrelevantQueryRate >= report.Static.IrrelevantQueryRate {
		t.Fatalf("adaptive=%#v static=%#v", report.Adaptive, report.Static)
	}
	markdown := Markdown(report)
	if !strings.Contains(markdown, "25 releases") || !strings.Contains(markdown, "**TRUE**") {
		t.Fatalf("markdown=%s", markdown)
	}
	if data, err := JSON(report); err != nil || !strings.Contains(string(data), "quality_gate") {
		t.Fatalf("json=%s err=%v", data, err)
	}
}

func TestDelayedScenarioIsDetectedByRealPipeline(t *testing.T) {
	data := corpus.Load()
	var delayed corpus.Scenario
	for _, scenario := range data.Scenarios {
		if scenario.Class == "delayed" && scenario.FailureStart >= 90*time.Minute {
			delayed = scenario
			break
		}
	}
	_, receipt, err := RunScenario(context.Background(), data, delayed)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Verdict.State != domain.HealthUnhealthy || !receipt.Verdict.Escalate {
		t.Fatalf("verdict=%#v", receipt.Verdict)
	}
}
