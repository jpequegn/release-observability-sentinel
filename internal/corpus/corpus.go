package corpus

import (
	"fmt"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

type Scenario struct {
	ID               string                 `json:"id"`
	Class            string                 `json:"class"`
	Release          domain.ReleaseEnvelope `json:"release"`
	AffectedServices []string               `json:"affected_services"`
	UsefulSignals    []string               `json:"useful_signals"`
	FailureStart     time.Duration          `json:"failure_start"`
	FailureEnd       time.Duration          `json:"failure_end"`
	ExpectedVerdict  domain.HealthState     `json:"expected_verdict"`
	ShouldEscalate   bool                   `json:"should_escalate"`
}

type Corpus struct {
	Version   string                  `json:"version"`
	Fictional bool                    `json:"fictional"`
	Services  []domain.ServiceContext `json:"services"`
	Scenarios []Scenario              `json:"scenarios"`
}

func Load() Corpus {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	services := []domain.ServiceContext{
		service("checkout", "team-orchid", []string{"currency", "inventory"}, "checkout_success_ratio", "checkout_latency_seconds"),
		service("currency", "team-juniper", nil, "currency_success_ratio", "currency_quote_seconds"),
		service("stream-router", "team-quartz", []string{"ledger", "analytics"}, "stream_delivery_ratio", "stream_partition_lag"),
		service("inventory", "team-lumen", []string{"stream-router"}, "inventory_success_ratio", "inventory_reservation_seconds"),
		service("identity", "team-cedar", []string{"notifications"}, "identity_success_ratio", "identity_auth_failures_total"),
		service("notifications", "team-nimbus", nil, "notification_success_ratio", "notification_delivery_seconds"),
		service("ledger", "team-saffron", []string{"identity"}, "ledger_balance_ratio", "ledger_unbalanced_batches_total"),
		service("analytics", "team-harbor", []string{"stream-router"}, "analytics_freshness_ratio", "analytics_partition_freshness_seconds"),
	}
	type spec struct {
		service, class string
		kind           domain.ChangeKind
		verdict        domain.HealthState
		start, end     time.Duration
	}
	specs := []spec{
		{"checkout", "normal", domain.ChangeApplication, domain.HealthHealthy, 0, 0},
		{"currency", "immediate", domain.ChangeDependency, domain.HealthUnhealthy, 0, 20 * time.Minute},
		{"stream-router", "delayed", domain.ChangeInfrastructure, domain.HealthUnhealthy, 45 * time.Minute, 2 * time.Hour},
		{"inventory", "normal", domain.ChangeFlag, domain.HealthHealthy, 0, 0},
		{"identity", "intermittent", domain.ChangeApplication, domain.HealthAmbiguous, 20 * time.Minute, 90 * time.Minute},
		{"notifications", "unrelated", domain.ChangeSchema, domain.HealthHealthy, 0, 0},
		{"ledger", "missing", domain.ChangeInfrastructure, domain.HealthMissingTelemetry, 0, 0},
		{"analytics", "immediate", domain.ChangeSchema, domain.HealthUnhealthy, 0, time.Hour},
		{"checkout", "delayed", domain.ChangeFlag, domain.HealthUnhealthy, time.Hour, 3 * time.Hour},
		{"currency", "normal", domain.ChangeApplication, domain.HealthHealthy, 0, 0},
		{"stream-router", "intermittent", domain.ChangeDependency, domain.HealthAmbiguous, 30 * time.Minute, 2 * time.Hour},
		{"inventory", "immediate", domain.ChangeSchema, domain.HealthUnhealthy, 0, 40 * time.Minute},
		{"identity", "normal", domain.ChangeFlag, domain.HealthHealthy, 0, 0},
		{"notifications", "query_failure", domain.ChangeInfrastructure, domain.HealthQueryFailure, 0, 0},
		{"ledger", "delayed", domain.ChangeApplication, domain.HealthUnhealthy, 90 * time.Minute, 4 * time.Hour},
		{"analytics", "normal", domain.ChangeDependency, domain.HealthHealthy, 0, 0},
		{"checkout", "ambiguous", domain.ChangeInfrastructure, domain.HealthAmbiguous, 10 * time.Minute, 80 * time.Minute},
		{"currency", "immediate", domain.ChangeFlag, domain.HealthUnhealthy, 0, 30 * time.Minute},
		{"stream-router", "normal", domain.ChangeSchema, domain.HealthHealthy, 0, 0},
		{"inventory", "intermittent", domain.ChangeApplication, domain.HealthAmbiguous, 15 * time.Minute, 2 * time.Hour},
		{"identity", "delayed", domain.ChangeDependency, domain.HealthUnhealthy, 50 * time.Minute, 3 * time.Hour},
		{"notifications", "normal", domain.ChangeApplication, domain.HealthHealthy, 0, 0},
		{"ledger", "immediate", domain.ChangeFlag, domain.HealthUnhealthy, 0, 45 * time.Minute},
		{"analytics", "missing", domain.ChangeInfrastructure, domain.HealthMissingTelemetry, 0, 0},
		{"checkout", "normal", domain.ChangeSchema, domain.HealthHealthy, 0, 0},
	}
	scenarios := make([]Scenario, 0, len(specs))
	for i, item := range specs {
		deployed := base.Add(time.Duration(i) * 24 * time.Hour)
		releaseID := fmt.Sprintf("rel-%03d", i+1)
		evidence := domain.EvidenceRef{ID: "ev-" + releaseID, URI: "fixture://releases/" + releaseID, Digest: digestOf(releaseID), Recorded: deployed}
		changed := domain.ChangedItem{Path: changedPath(item.service, item.kind), Kind: item.kind, Before: "v1", After: "v2", EvidenceID: evidence.ID}
		release := domain.ReleaseEnvelope{ID: releaseID, Repository: "fictional/" + item.service, CommitSHA: digestOf(releaseID)[:40], Artifact: item.service + ":v2", Environment: "production", DeployedAt: deployed, Changed: []domain.ChangedItem{changed}, Owner: ownerOf(services, item.service), RiskClass: "medium", RollbackRef: "rollback-" + releaseID, Evidence: []domain.EvidenceRef{evidence}}
		signals := append([]string(nil), serviceByID(services, item.service).Signals[domain.BackendPrometheus]...)
		scenarios = append(scenarios, Scenario{ID: fmt.Sprintf("scenario-%03d", i+1), Class: item.class, Release: release, AffectedServices: []string{item.service}, UsefulSignals: signals, FailureStart: item.start, FailureEnd: item.end, ExpectedVerdict: item.verdict, ShouldEscalate: item.verdict != domain.HealthHealthy})
	}
	return Corpus{Version: "2026-08-17.1", Fictional: true, Services: services, Scenarios: scenarios}
}

func (c Corpus) Validate() error {
	if !c.Fictional || len(c.Services) != 8 || len(c.Scenarios) < 25 {
		return fmt.Errorf("corpus requires fictional marker, 8 services, and at least 25 scenarios")
	}
	known := map[string]bool{}
	for _, service := range c.Services {
		known[service.ID] = true
	}
	delayed := 0
	for _, scenario := range c.Scenarios {
		if err := scenario.Release.Validate(); err != nil {
			return fmt.Errorf("%s: %w", scenario.ID, err)
		}
		for _, affected := range scenario.AffectedServices {
			if !known[affected] {
				return fmt.Errorf("%s references unknown service %s", scenario.ID, affected)
			}
		}
		if scenario.FailureStart > 0 {
			delayed++
		}
	}
	if delayed < 3 {
		return fmt.Errorf("corpus requires at least three delayed or intermittent regressions")
	}
	return nil
}

func service(id, owner string, deps []string, ratio, latency string) domain.ServiceContext {
	recorded := time.Date(2025, 12, 31, 12, 0, 0, 0, time.UTC)
	return domain.ServiceContext{ID: id, Owner: owner, Dependencies: deps, SLO: ratio + ">=0.999", Signals: map[domain.Backend][]string{domain.BackendPrometheus: {ratio, latency}, domain.BackendTempo: {id + " span"}, domain.BackendLoki: {id + " error"}}, AllowedBackends: []domain.Backend{domain.BackendPrometheus, domain.BackendTempo, domain.BackendLoki}, Sensitivity: "internal", RecordedAt: recorded, Evidence: domain.EvidenceRef{ID: "ctx-" + id, URI: "fixture://context/" + id, Digest: digestOf(id), Recorded: recorded}}
}

func serviceByID(services []domain.ServiceContext, id string) domain.ServiceContext {
	for _, service := range services {
		if service.ID == id {
			return service
		}
	}
	return domain.ServiceContext{}
}

func ownerOf(services []domain.ServiceContext, id string) string {
	return serviceByID(services, id).Owner
}

func changedPath(service string, kind domain.ChangeKind) string {
	switch kind {
	case domain.ChangeFlag:
		return "config/flags/" + service + ".yaml"
	case domain.ChangeSchema:
		return "migrations/" + service + "/002.sql"
	case domain.ChangeDependency:
		return "services/" + service + "/go.mod"
	case domain.ChangeInfrastructure:
		return "infra/" + service + "/deployment.yaml"
	default:
		return "services/" + service + "/handler.go"
	}
}

func digestOf(value string) string {
	digest, _ := domain.Digest(value)
	return digest
}
