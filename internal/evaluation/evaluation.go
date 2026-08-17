package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
	"github.com/jpequegn/release-observability-sentinel/internal/ledger"
	"github.com/jpequegn/release-observability-sentinel/internal/mapping"
	"github.com/jpequegn/release-observability-sentinel/internal/planning"
	"github.com/jpequegn/release-observability-sentinel/internal/telemetry"
	"github.com/jpequegn/release-observability-sentinel/internal/watch"
)

type Metrics struct {
	AffectedServiceRecall float64 `json:"affected_service_recall"`
	UsefulQueryPrecision  float64 `json:"useful_query_precision"`
	MissedRegressions     int     `json:"missed_regressions"`
	IrrelevantQueryRate   float64 `json:"irrelevant_query_rate"`
	EscalationF1          float64 `json:"escalation_f1"`
	AverageLatencyMinutes float64 `json:"average_latency_minutes"`
	AverageQueryCost      float64 `json:"average_query_cost"`
}

type QualityGate struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

type Report struct {
	Schema        string      `json:"schema_version"`
	CorpusVersion string      `json:"corpus_version"`
	ReleaseCount  int         `json:"release_count"`
	ServiceCount  int         `json:"service_count"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Adaptive      Metrics     `json:"release_specific"`
	Static        Metrics     `json:"static_baseline"`
	Gate          QualityGate `json:"quality_gate"`
}

type counters struct {
	affectedFound int
	affectedTotal int
	usefulQueries int
	totalQueries  int
	missed        int
	tp, fp, fn    int
	latency       float64
	cost          int
	releases      int
}

func Run(ctx context.Context) (Report, error) {
	data := corpus.Load()
	if err := data.Validate(); err != nil {
		return Report{}, err
	}
	adaptive := counters{}
	baseline := counters{}
	for _, scenario := range data.Scenarios {
		plan, receipt, err := RunScenario(ctx, data, scenario)
		if err != nil {
			return Report{}, fmt.Errorf("replay %s: %w", scenario.ID, err)
		}
		accumulateAdaptive(&adaptive, scenario, plan, receipt)
		accumulateBaseline(&baseline, scenario)
	}
	report := Report{Schema: "replay-evaluation-v1", CorpusVersion: data.Version, ReleaseCount: len(data.Scenarios), ServiceCount: len(data.Services), GeneratedAt: data.Scenarios[len(data.Scenarios)-1].Release.DeployedAt.Add(4 * time.Hour), Adaptive: adaptive.metrics(), Static: baseline.metrics()}
	report.Gate = compare(report.Adaptive, report.Static)
	return report, nil
}

func BuildPlan(ctx context.Context, data corpus.Corpus, scenario corpus.Scenario) (domain.WatchPlan, error) {
	packets := make([]mapping.ContextPacket, 0, len(data.Services))
	for _, service := range data.Services {
		packets = append(packets, mapping.ContextPacket{Service: service.ID, QuestionTime: scenario.Release.DeployedAt, State: "current", Facts: []mapping.ContextFact{{FactID: "eval-" + service.ID, Service: service.ID, Predicate: "uses_metric", Value: service.Signals[domain.BackendPrometheus][0], State: "current", Authority: 100, RecordedAt: scenario.Release.DeployedAt.Add(-time.Minute), ValidFrom: service.RecordedAt, Source: service.Evidence}}})
	}
	mapped, err := mapping.New(data.Services, nil).Map(ctx, scenario.Release, packets)
	if err != nil {
		return domain.WatchPlan{}, err
	}
	plan, decision, err := planning.New(planning.DefaultLimits()).Build(scenario.Release, mapped)
	if err != nil {
		return domain.WatchPlan{}, err
	}
	if !decision.Allowed {
		return domain.WatchPlan{}, fmt.Errorf("plan denied: %#v", decision.Denials)
	}
	return plan, nil
}

func RunScenario(ctx context.Context, data corpus.Corpus, scenario corpus.Scenario) (domain.WatchPlan, domain.Receipt, error) {
	return RunScenarioWithLedger(ctx, data, scenario, "")
}

func RunScenarioWithLedger(ctx context.Context, data corpus.Corpus, scenario corpus.Scenario, ledgerPath string) (domain.WatchPlan, domain.Receipt, error) {
	plan, err := BuildPlan(ctx, data, scenario)
	if err != nil {
		return domain.WatchPlan{}, domain.Receipt{}, err
	}
	plan, err = planning.Transition(plan, domain.ApprovalApproved, "evaluation-runner", "deterministic replay", scenario.Release.DeployedAt.Add(time.Minute), planning.DefaultLimits())
	if err != nil {
		return domain.WatchPlan{}, domain.Receipt{}, err
	}
	adapters := map[domain.Backend]telemetry.Adapter{}
	for _, backend := range []domain.Backend{domain.BackendPrometheus, domain.BackendTempo, domain.BackendLoki} {
		adapters[backend] = telemetry.FixtureAdapter{Kind: backend, Scenario: scenario}
	}
	var receiptLedger *ledger.Ledger
	if ledgerPath != "" {
		receiptLedger = &ledger.Ledger{Path: ledgerPath}
	}
	runner := watch.Runner{Executor: telemetry.Executor{Adapters: adapters}, Clock: watch.NewVirtualClock(scenario.Release.DeployedAt), Ledger: receiptLedger}
	receipt, err := runner.Run(ctx, scenario.Release, plan)
	return plan, receipt, err
}

func FindScenario(data corpus.Corpus, id string) (corpus.Scenario, bool) {
	for _, scenario := range data.Scenarios {
		if scenario.ID == id || scenario.Release.ID == id {
			return scenario, true
		}
	}
	return corpus.Scenario{}, false
}

func accumulateAdaptive(result *counters, scenario corpus.Scenario, plan domain.WatchPlan, receipt domain.Receipt) {
	affected := stringSet(scenario.AffectedServices)
	plannedServices := map[string]bool{}
	for _, query := range plan.Queries {
		plannedServices[query.Service] = true
		result.totalQueries++
		if affected[query.Service] {
			result.usefulQueries++
		}
	}
	for service := range affected {
		result.affectedTotal++
		if plannedServices[service] {
			result.affectedFound++
		}
	}
	predicted := receipt.Verdict.Escalate
	accumulateClassification(result, scenario.ShouldEscalate, predicted)
	if scenario.ShouldEscalate && !predicted {
		result.missed++
	}
	result.latency += receipt.Generated.Sub(scenario.Release.DeployedAt).Minutes()
	result.cost += len(receipt.Checks)
	result.releases++
}

var staticServices = map[string]bool{"checkout": true, "identity": true, "stream-router": true}

func accumulateBaseline(result *counters, scenario corpus.Scenario) {
	affected := stringSet(scenario.AffectedServices)
	covered := false
	for service := range affected {
		result.affectedTotal++
		if staticServices[service] {
			result.affectedFound++
			covered = true
		}
	}
	for service := range staticServices {
		result.totalQueries++
		if affected[service] {
			result.usefulQueries++
		}
	}
	visibleAtDeploy := scenario.FailureStart == 0
	predicted := covered && visibleAtDeploy && scenario.ShouldEscalate
	accumulateClassification(result, scenario.ShouldEscalate, predicted)
	if scenario.ShouldEscalate && !predicted {
		result.missed++
	}
	result.cost += len(staticServices)
	result.releases++
}

func accumulateClassification(result *counters, expected, predicted bool) {
	switch {
	case expected && predicted:
		result.tp++
	case !expected && predicted:
		result.fp++
	case expected && !predicted:
		result.fn++
	}
}

func (c counters) metrics() Metrics {
	precision := ratio(c.usefulQueries, c.totalQueries)
	return Metrics{AffectedServiceRecall: ratio(c.affectedFound, c.affectedTotal), UsefulQueryPrecision: precision, MissedRegressions: c.missed, IrrelevantQueryRate: 1 - precision, EscalationF1: f1(c.tp, c.fp, c.fn), AverageLatencyMinutes: c.latency / float64(c.releases), AverageQueryCost: float64(c.cost) / float64(c.releases)}
}

func compare(adaptive, baseline Metrics) QualityGate {
	failures := []string{}
	if adaptive.AffectedServiceRecall <= baseline.AffectedServiceRecall {
		failures = append(failures, "affected-service recall did not improve")
	}
	if adaptive.MissedRegressions >= baseline.MissedRegressions {
		failures = append(failures, "missed regressions did not decrease")
	}
	if adaptive.IrrelevantQueryRate >= baseline.IrrelevantQueryRate {
		failures = append(failures, "irrelevant-query rate did not decrease")
	}
	if adaptive.EscalationF1 <= baseline.EscalationF1 {
		failures = append(failures, "escalation F1 did not improve")
	}
	return QualityGate{Passed: len(failures) == 0, Failures: failures}
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func f1(tp, fp, fn int) float64 {
	denominator := 2*tp + fp + fn
	if denominator == 0 {
		return 0
	}
	return float64(2*tp) / float64(denominator)
}

func stringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}

func JSON(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func Markdown(report Report) string {
	rows := []struct {
		name     string
		adaptive string
		static   string
	}{
		{"Affected-service recall", percent(report.Adaptive.AffectedServiceRecall), percent(report.Static.AffectedServiceRecall)},
		{"Useful-query precision", percent(report.Adaptive.UsefulQueryPrecision), percent(report.Static.UsefulQueryPrecision)},
		{"Missed regressions", fmt.Sprint(report.Adaptive.MissedRegressions), fmt.Sprint(report.Static.MissedRegressions)},
		{"Irrelevant-query rate", percent(report.Adaptive.IrrelevantQueryRate), percent(report.Static.IrrelevantQueryRate)},
		{"Escalation F1", fmt.Sprintf("%.3f", report.Adaptive.EscalationF1), fmt.Sprintf("%.3f", report.Static.EscalationF1)},
		{"Average detection latency", fmt.Sprintf("%.1f min", report.Adaptive.AverageLatencyMinutes), fmt.Sprintf("%.1f min", report.Static.AverageLatencyMinutes)},
		{"Average query cost", fmt.Sprintf("%.1f", report.Adaptive.AverageQueryCost), fmt.Sprintf("%.1f", report.Static.AverageQueryCost)},
	}
	var output strings.Builder
	fmt.Fprintf(&output, "# Replay evaluation\n\nCorpus `%s`: %d releases across %d services.\n\n", report.CorpusVersion, report.ReleaseCount, report.ServiceCount)
	output.WriteString("| Metric | Release-specific | Static baseline |\n|---|---:|---:|\n")
	for _, row := range rows {
		fmt.Fprintf(&output, "| %s | %s | %s |\n", row.name, row.adaptive, row.static)
	}
	fmt.Fprintf(&output, "\n## Quality gate\n\n**%s**\n", strings.ToUpper(fmt.Sprint(report.Gate.Passed)))
	if len(report.Gate.Failures) > 0 {
		sort.Strings(report.Gate.Failures)
		for _, failure := range report.Gate.Failures {
			fmt.Fprintf(&output, "\n- %s", failure)
		}
		output.WriteString("\n")
	}
	return output.String()
}

func percent(value float64) string { return fmt.Sprintf("%.1f%%", value*100) }
