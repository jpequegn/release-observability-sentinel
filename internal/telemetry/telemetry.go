package telemetry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

const GraderVersion = "deterministic-grader-v1"

type RawResult struct {
	Values    map[string]float64 `json:"values,omitempty"`
	Messages  []string           `json:"messages,omitempty"`
	Truncated bool               `json:"truncated"`
}

type Adapter interface {
	Backend() domain.Backend
	Query(context.Context, domain.QuerySpec, time.Time) (RawResult, error)
}

type FixtureAdapter struct {
	Kind     domain.Backend
	Scenario corpus.Scenario
	Delay    time.Duration
}

func (a FixtureAdapter) Backend() domain.Backend { return a.Kind }

func (a FixtureAdapter) Query(ctx context.Context, query domain.QuerySpec, windowEnd time.Time) (RawResult, error) {
	if query.Backend != a.Kind {
		return RawResult{}, fmt.Errorf("adapter %s cannot execute %s query", a.Kind, query.Backend)
	}
	if a.Delay > 0 {
		select {
		case <-ctx.Done():
			return RawResult{}, ctx.Err()
		case <-time.After(a.Delay):
		}
	}
	if a.Scenario.Class == "query_failure" {
		return RawResult{}, errors.New("fixture telemetry backend unavailable")
	}
	if a.Scenario.Class == "missing" {
		return RawResult{}, nil
	}
	offset := windowEnd.Sub(a.Scenario.Release.DeployedAt)
	active := failureActive(a.Scenario, offset)
	ambiguous := a.Scenario.Class == "ambiguous"
	switch a.Kind {
	case domain.BackendPrometheus:
		if ambiguous {
			return RawResult{Values: map[string]float64{"health_ratio": 0.991, "error_rate": 0.009}}, nil
		}
		if active {
			return RawResult{Values: map[string]float64{"health_ratio": 0.94, "error_rate": 0.06}}, nil
		}
		return RawResult{Values: map[string]float64{"health_ratio": 0.9999, "error_rate": 0.0001}}, nil
	case domain.BackendTempo:
		if ambiguous {
			return RawResult{Messages: []string{"WARN trace latency near threshold"}}, nil
		}
		if active {
			return RawResult{Messages: []string{"ERROR trace latency exceeded SLO"}}, nil
		}
		return RawResult{Messages: []string{"OK trace latency within SLO"}}, nil
	case domain.BackendLoki:
		if active || ambiguous {
			level := "ERROR"
			if ambiguous {
				level = "WARN"
			}
			return RawResult{Messages: []string{level + " request failed authorization=Bearer-fixture email=fictional@example.test"}}, nil
		}
		return RawResult{Messages: []string{"INFO release healthy token=fixture-secret"}}, nil
	default:
		return RawResult{}, fmt.Errorf("unsupported backend %s", a.Kind)
	}
}

type Executor struct {
	Adapters map[domain.Backend]Adapter
	Now      func() time.Time
}

func (e Executor) Execute(ctx context.Context, query domain.QuerySpec, windowEnd time.Time) domain.Observation {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	observation := domain.Observation{QueryID: query.ID, Backend: query.Backend, WindowStart: windowEnd.Add(-query.Range), WindowEnd: windowEnd, RetrievedAt: now().UTC()}
	observation.ID, _ = domain.StableID("obs", struct {
		Query string
		End   time.Time
	}{query.ID, windowEnd})
	adapter, ok := e.Adapters[query.Backend]
	if !ok {
		observation.Error = "no adapter for backend"
		return observation
	}
	queryCtx, cancel := context.WithTimeout(ctx, query.Deadline)
	defer cancel()
	raw, err := adapter.Query(queryCtx, query, windowEnd)
	if err != nil {
		observation.Error = err.Error()
		return observation
	}
	if len(raw.Messages) > query.ResultLimit {
		raw.Messages = raw.Messages[:query.ResultLimit]
		raw.Truncated = true
	}
	redactedMessages, redacted := redact(raw.Messages)
	raw.Messages = redactedMessages
	rawDigest, _ := domain.Digest(raw)
	observation.RawDigest = rawDigest
	observation.Values = raw.Values
	observation.Messages = raw.Messages
	observation.Truncated = raw.Truncated
	observation.Redacted = redacted
	return observation
}

func Grade(observations []domain.Observation, evaluatedAt time.Time) domain.Verdict {
	ids := make([]string, 0, len(observations))
	healthy, unhealthy, ambiguous, missing, failures := 0, 0, 0, 0, 0
	for _, observation := range observations {
		ids = append(ids, observation.ID)
		if observation.Error != "" {
			failures++
			continue
		}
		if len(observation.Values) == 0 && len(observation.Messages) == 0 {
			missing++
			continue
		}
		state := classify(observation)
		switch state {
		case domain.HealthUnhealthy:
			unhealthy++
		case domain.HealthAmbiguous:
			ambiguous++
		default:
			healthy++
		}
	}
	verdict := domain.Verdict{ObservationIDs: ids, GraderVersion: GraderVersion, EvaluatedAt: evaluatedAt}
	switch {
	case len(observations) == 0:
		verdict.State, verdict.Reason = domain.HealthInsufficientEvidence, "no observations were supplied"
	case failures > 0:
		verdict.State, verdict.Reason, verdict.Escalate = domain.HealthQueryFailure, "one or more telemetry queries failed", true
	case missing == len(observations):
		verdict.State, verdict.Reason, verdict.Escalate = domain.HealthMissingTelemetry, "all requested telemetry is missing", true
	case ambiguous > 0 || (healthy > 0 && unhealthy > 0):
		verdict.State, verdict.Reason, verdict.Escalate = domain.HealthAmbiguous, "telemetry signals disagree or are near thresholds", true
	case unhealthy > 0:
		verdict.State, verdict.Reason, verdict.Escalate = domain.HealthUnhealthy, "deterministic SLO or error invariant failed", true
	case healthy > 0:
		verdict.State, verdict.Reason = domain.HealthHealthy, "all observed deterministic invariants passed"
	default:
		verdict.State, verdict.Reason, verdict.Escalate = domain.HealthInsufficientEvidence, "available evidence could not be graded", true
	}
	return verdict
}

func classify(observation domain.Observation) domain.HealthState {
	if ratio, ok := observation.Values["health_ratio"]; ok && ratio < 0.99 {
		return domain.HealthUnhealthy
	}
	if rate, ok := observation.Values["error_rate"]; ok && rate > 0.01 {
		return domain.HealthUnhealthy
	}
	if ratio, ok := observation.Values["health_ratio"]; ok && ratio < 0.995 {
		return domain.HealthAmbiguous
	}
	if rate, ok := observation.Values["error_rate"]; ok && rate > 0.005 {
		return domain.HealthAmbiguous
	}
	for _, message := range observation.Messages {
		upper := strings.ToUpper(message)
		if strings.Contains(upper, "ERROR") {
			return domain.HealthUnhealthy
		}
		if strings.Contains(upper, "WARN") {
			return domain.HealthAmbiguous
		}
	}
	return domain.HealthHealthy
}

func failureActive(scenario corpus.Scenario, offset time.Duration) bool {
	if scenario.ExpectedVerdict == domain.HealthHealthy || scenario.FailureEnd == 0 {
		return false
	}
	if offset < scenario.FailureStart || offset > scenario.FailureEnd {
		return false
	}
	if scenario.Class == "intermittent" {
		return (int(offset.Minutes())/15)%2 == 1
	}
	return true
}

var sensitivePattern = regexp.MustCompile(`(?i)(authorization|token|password|secret|email)=([^ ]+)`)

func redact(messages []string) ([]string, bool) {
	result := make([]string, len(messages))
	redacted := false
	for i, message := range messages {
		result[i] = sensitivePattern.ReplaceAllStringFunc(message, func(match string) string {
			redacted = true
			key := strings.SplitN(match, "=", 2)[0]
			return key + "=[REDACTED]"
		})
	}
	return result, redacted
}
