package planning

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/domain"
	"github.com/jpequegn/release-observability-sentinel/internal/mapping"
)

const PolicyVersion = "watch-policy-v1"

type Limits struct {
	MaxQueries      int
	MaxDuration     time.Duration
	MaxRange        time.Duration
	MinCadence      time.Duration
	MaxCost         int
	AllowedBackends map[domain.Backend]bool
	SensitiveTokens []string
}

func DefaultLimits() Limits {
	return Limits{MaxQueries: 12, MaxDuration: 3 * time.Hour, MaxRange: 30 * time.Minute, MinCadence: 5 * time.Minute, MaxCost: 24, AllowedBackends: map[domain.Backend]bool{domain.BackendPrometheus: true, domain.BackendTempo: true, domain.BackendLoki: true}, SensitiveTokens: []string{"password", "authorization", "secret", "token="}}
}

type Denial struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	QueryID string `json:"query_id,omitempty"`
}

type PolicyDecision struct {
	Allowed       bool     `json:"allowed"`
	PolicyVersion string   `json:"policy_version"`
	Denials       []Denial `json:"denials,omitempty"`
}

type Planner struct {
	Limits Limits
}

func New(limits Limits) Planner { return Planner{Limits: limits} }

func (p Planner) Build(release domain.ReleaseEnvelope, mapped mapping.Result) (domain.WatchPlan, PolicyDecision, error) {
	if mapped.ReleaseID != release.ID {
		return domain.WatchPlan{}, PolicyDecision{}, errors.New("mapping release ID does not match release")
	}
	services := append([]mapping.MappedService(nil), mapped.Services...)
	directService := release.Repository[strings.LastIndex(release.Repository, "/")+1:]
	sort.SliceStable(services, func(i, j int) bool {
		if services[i].Service.ID == directService {
			return true
		}
		if services[j].Service.ID == directService {
			return false
		}
		return services[i].Service.ID < services[j].Service.ID
	})
	hypotheses := make([]domain.FailureHypothesis, 0, len(services))
	queries := []domain.QuerySpec{}
	for _, item := range services {
		hypothesisID, err := domain.StableID("hyp", struct {
			Release string
			Service string
			Reasons []string
		}{release.ID, item.Service.ID, item.Reasons})
		if err != nil {
			return domain.WatchPlan{}, PolicyDecision{}, err
		}
		hypothesis := domain.FailureHypothesis{ID: hypothesisID, Service: item.Service.ID, Statement: fmt.Sprintf("Release %s may degrade %s because %s", release.ID, item.Service.ID, strings.Join(item.Reasons, "; ")), ExpectedHealthy: item.Service.SLO, FailureMode: "release-correlated SLO or error regression", ReleaseEvidence: append([]domain.EvidenceRef(nil), item.ReleaseEvidence...), ContextEvidence: append([]domain.EvidenceRef(nil), item.ContextEvidence...), MissingContext: append([]string(nil), item.MissingContext...), ConfidenceBasis: "deterministic path, dependency, and context mapping"}
		hypotheses = append(hypotheses, hypothesis)
		for _, backend := range []domain.Backend{domain.BackendPrometheus, domain.BackendTempo, domain.BackendLoki} {
			if len(queries) >= p.Limits.MaxQueries {
				break
			}
			signals := item.Signals[backend]
			if len(signals) == 0 {
				continue
			}
			expression := signals[0]
			queryID, err := domain.StableID("query", struct {
				Release, Service string
				Backend          domain.Backend
				Expression       string
			}{release.ID, item.Service.ID, backend, expression})
			if err != nil {
				return domain.WatchPlan{}, PolicyDecision{}, err
			}
			evidenceIDs := []string{}
			for _, ref := range append(item.ReleaseEvidence, item.ContextEvidence...) {
				evidenceIDs = append(evidenceIDs, ref.ID)
			}
			sort.Strings(evidenceIDs)
			queries = append(queries, domain.QuerySpec{ID: queryID, Backend: backend, Expression: expression, Service: item.Service.ID, HypothesisID: hypothesisID, EvidenceIDs: evidenceIDs, Range: 15 * time.Minute, Deadline: 5 * time.Second, ResultLimit: 100, EstimatedCost: 1})
		}
	}
	planID, err := domain.StableID("plan", struct {
		Release string
		Queries []domain.QuerySpec
	}{release.ID, queries})
	if err != nil {
		return domain.WatchPlan{}, PolicyDecision{}, err
	}
	plan := domain.WatchPlan{ID: planID, ReleaseID: release.ID, CreatedAt: release.DeployedAt, Hypotheses: hypotheses, Queries: queries, CheckOffsets: []time.Duration{0, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour}, MaxDuration: p.Limits.MaxDuration, MaxQueries: p.Limits.MaxQueries, MaxCost: p.Limits.MaxCost, StopConditions: []string{"unhealthy", "cancelled", "max_duration"}, Owner: release.Owner, PolicyVersion: PolicyVersion, Approval: domain.Approval{State: domain.ApprovalProposed, Version: 1, UpdatedAt: release.DeployedAt}}
	decision := Evaluate(plan, p.Limits)
	if !decision.Allowed {
		plan.Approval.State = domain.ApprovalAbstained
		plan.Approval.Reason = "plan failed policy"
	}
	return plan, decision, nil
}

func Evaluate(plan domain.WatchPlan, limits Limits) PolicyDecision {
	denials := []Denial{}
	if len(plan.Queries) == 0 {
		denials = append(denials, Denial{Code: "no_queries", Message: "plan has no evidence queries"})
	}
	if len(plan.Queries) > limits.MaxQueries {
		denials = append(denials, Denial{Code: "query_budget", Message: "query count exceeds policy"})
	}
	if plan.MaxDuration > limits.MaxDuration {
		denials = append(denials, Denial{Code: "duration_budget", Message: "watch duration exceeds policy"})
	}
	for i := 1; i < len(plan.CheckOffsets); i++ {
		if plan.CheckOffsets[i]-plan.CheckOffsets[i-1] < limits.MinCadence {
			denials = append(denials, Denial{Code: "cadence", Message: "check cadence is too frequent"})
			break
		}
	}
	hypotheses := map[string]domain.FailureHypothesis{}
	for _, hypothesis := range plan.Hypotheses {
		hypotheses[hypothesis.ID] = hypothesis
		if len(hypothesis.MissingContext) > 0 {
			denials = append(denials, Denial{Code: "missing_context", Message: "hypothesis lacks current context evidence"})
		}
	}
	cost := 0
	for _, query := range plan.Queries {
		cost += query.EstimatedCost
		if !limits.AllowedBackends[query.Backend] {
			denials = append(denials, Denial{Code: "backend", Message: "backend is not allowlisted", QueryID: query.ID})
		}
		if query.Range <= 0 || query.Range > limits.MaxRange || query.Deadline <= 0 || query.ResultLimit <= 0 {
			denials = append(denials, Denial{Code: "query_bounds", Message: "query range, deadline, or result limit is invalid", QueryID: query.ID})
		}
		hypothesis, ok := hypotheses[query.HypothesisID]
		if !ok || len(query.EvidenceIDs) == 0 || len(hypothesis.ReleaseEvidence) == 0 {
			denials = append(denials, Denial{Code: "evidence_link", Message: "query lacks hypothesis and release evidence", QueryID: query.ID})
		}
		lower := strings.ToLower(query.Expression)
		for _, unsafe := range []string{"delete ", "update ", "drop ", "insert "} {
			if strings.Contains(lower, unsafe) {
				denials = append(denials, Denial{Code: "write_query", Message: "write-like query is forbidden", QueryID: query.ID})
			}
		}
		for _, sensitive := range limits.SensitiveTokens {
			if strings.Contains(lower, sensitive) {
				denials = append(denials, Denial{Code: "sensitive_field", Message: "query references a sensitive field", QueryID: query.ID})
			}
		}
	}
	if cost > limits.MaxCost {
		denials = append(denials, Denial{Code: "cost_budget", Message: "estimated query cost exceeds policy"})
	}
	return PolicyDecision{Allowed: len(denials) == 0, PolicyVersion: PolicyVersion, Denials: denials}
}

func Transition(plan domain.WatchPlan, target domain.ApprovalState, actor, reason string, at time.Time, limits Limits) (domain.WatchPlan, error) {
	if actor == "" || at.IsZero() {
		return plan, errors.New("approval actor and timestamp are required")
	}
	terminal := map[domain.ApprovalState]bool{domain.ApprovalRejected: true, domain.ApprovalCancelled: true, domain.ApprovalAbstained: true}
	if terminal[plan.Approval.State] {
		return plan, fmt.Errorf("cannot transition terminal approval state %s", plan.Approval.State)
	}
	if target == domain.ApprovalApproved {
		decision := Evaluate(plan, limits)
		if !decision.Allowed {
			return plan, errors.New("cannot approve a plan that fails policy")
		}
	}
	allowed := map[domain.ApprovalState]bool{domain.ApprovalApproved: true, domain.ApprovalEdited: true, domain.ApprovalRejected: true, domain.ApprovalCancelled: true, domain.ApprovalAbstained: true}
	if !allowed[target] {
		return plan, fmt.Errorf("unsupported approval transition to %s", target)
	}
	plan.Approval = domain.Approval{State: target, Actor: actor, Reason: reason, Version: plan.Approval.Version + 1, UpdatedAt: at}
	return plan, nil
}
