package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type ChangeKind string

const (
	ChangeApplication    ChangeKind = "application"
	ChangeFlag           ChangeKind = "flag"
	ChangeSchema         ChangeKind = "schema"
	ChangeDependency     ChangeKind = "dependency"
	ChangeInfrastructure ChangeKind = "infrastructure"
)

type Backend string

const (
	BackendPrometheus Backend = "prometheus"
	BackendTempo      Backend = "tempo"
	BackendLoki       Backend = "loki"
)

type HealthState string

const (
	HealthHealthy              HealthState = "healthy"
	HealthUnhealthy            HealthState = "unhealthy"
	HealthAmbiguous            HealthState = "ambiguous"
	HealthMissingTelemetry     HealthState = "missing_telemetry"
	HealthQueryFailure         HealthState = "query_failure"
	HealthInsufficientEvidence HealthState = "insufficient_evidence"
)

type ApprovalState string

const (
	ApprovalProposed  ApprovalState = "proposed"
	ApprovalApproved  ApprovalState = "approved"
	ApprovalEdited    ApprovalState = "edited"
	ApprovalRejected  ApprovalState = "rejected"
	ApprovalCancelled ApprovalState = "cancelled"
	ApprovalAbstained ApprovalState = "abstained"
)

type EvidenceRef struct {
	ID       string    `json:"id"`
	URI      string    `json:"uri"`
	Digest   string    `json:"digest"`
	Recorded time.Time `json:"recorded_at"`
}

type ReleaseEnvelope struct {
	ID          string        `json:"id"`
	Repository  string        `json:"repository"`
	CommitSHA   string        `json:"commit_sha"`
	Artifact    string        `json:"artifact"`
	Environment string        `json:"environment"`
	DeployedAt  time.Time     `json:"deployed_at"`
	Changed     []ChangedItem `json:"changed"`
	Owner       string        `json:"owner"`
	RiskClass   string        `json:"risk_class"`
	RollbackRef string        `json:"rollback_ref"`
	Evidence    []EvidenceRef `json:"evidence"`
}

type ChangedItem struct {
	Path       string     `json:"path"`
	Kind       ChangeKind `json:"kind"`
	Before     string     `json:"before,omitempty"`
	After      string     `json:"after,omitempty"`
	EvidenceID string     `json:"evidence_id"`
}

func (r ReleaseEnvelope) Validate() error {
	if r.ID == "" || r.Repository == "" || r.CommitSHA == "" || r.Environment == "" {
		return errors.New("release identity, repository, commit, and environment are required")
	}
	if r.DeployedAt.IsZero() || len(r.Changed) == 0 || len(r.Evidence) == 0 {
		return errors.New("release requires deployment time, changes, and evidence")
	}
	for _, changed := range r.Changed {
		if changed.Path == "" || changed.Kind == "" || changed.EvidenceID == "" {
			return errors.New("changed items require path, kind, and evidence")
		}
	}
	return nil
}

type ServiceContext struct {
	ID              string               `json:"id"`
	Owner           string               `json:"owner"`
	Dependencies    []string             `json:"dependencies"`
	SLO             string               `json:"slo"`
	Signals         map[Backend][]string `json:"signals"`
	AllowedBackends []Backend            `json:"allowed_backends"`
	Sensitivity     string               `json:"sensitivity"`
	RecordedAt      time.Time            `json:"recorded_at"`
	Evidence        EvidenceRef          `json:"evidence"`
}

type FailureHypothesis struct {
	ID              string        `json:"id"`
	Service         string        `json:"service"`
	Statement       string        `json:"statement"`
	ExpectedHealthy string        `json:"expected_healthy"`
	FailureMode     string        `json:"failure_mode"`
	ReleaseEvidence []EvidenceRef `json:"release_evidence"`
	ContextEvidence []EvidenceRef `json:"context_evidence"`
	MissingContext  []string      `json:"missing_context,omitempty"`
	ConfidenceBasis string        `json:"confidence_basis"`
}

type QuerySpec struct {
	ID            string        `json:"id"`
	Backend       Backend       `json:"backend"`
	Expression    string        `json:"expression"`
	Service       string        `json:"service"`
	HypothesisID  string        `json:"hypothesis_id"`
	EvidenceIDs   []string      `json:"evidence_ids"`
	Range         time.Duration `json:"range"`
	Deadline      time.Duration `json:"deadline"`
	ResultLimit   int           `json:"result_limit"`
	EstimatedCost int           `json:"estimated_cost"`
}

type WatchPlan struct {
	ID             string              `json:"id"`
	ReleaseID      string              `json:"release_id"`
	CreatedAt      time.Time           `json:"created_at"`
	Hypotheses     []FailureHypothesis `json:"hypotheses"`
	Queries        []QuerySpec         `json:"queries"`
	CheckOffsets   []time.Duration     `json:"check_offsets"`
	MaxDuration    time.Duration       `json:"max_duration"`
	MaxQueries     int                 `json:"max_queries"`
	MaxCost        int                 `json:"max_cost"`
	StopConditions []string            `json:"stop_conditions"`
	Owner          string              `json:"owner"`
	PolicyVersion  string              `json:"policy_version"`
	Approval       Approval            `json:"approval"`
}

type Approval struct {
	State     ApprovalState `json:"state"`
	Actor     string        `json:"actor,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	Version   int           `json:"version"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type Observation struct {
	ID          string             `json:"id"`
	QueryID     string             `json:"query_id"`
	Backend     Backend            `json:"backend"`
	WindowStart time.Time          `json:"window_start"`
	WindowEnd   time.Time          `json:"window_end"`
	RetrievedAt time.Time          `json:"retrieved_at"`
	RawDigest   string             `json:"raw_digest"`
	Values      map[string]float64 `json:"values,omitempty"`
	Messages    []string           `json:"messages,omitempty"`
	Truncated   bool               `json:"truncated"`
	Redacted    bool               `json:"redacted"`
	Error       string             `json:"error,omitempty"`
}

type Verdict struct {
	State          HealthState `json:"state"`
	Reason         string      `json:"reason"`
	ObservationIDs []string    `json:"observation_ids"`
	GraderVersion  string      `json:"grader_version"`
	EvaluatedAt    time.Time   `json:"evaluated_at"`
	Escalate       bool        `json:"escalate"`
}

type Receipt struct {
	Release   ReleaseEnvelope `json:"release"`
	Plan      WatchPlan       `json:"plan"`
	Checks    []Observation   `json:"checks"`
	Verdict   Verdict         `json:"verdict"`
	Generated time.Time       `json:"generated_at"`
	Schema    string          `json:"schema_version"`
}

func Digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal digest input: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func StableID(prefix string, value any) (string, error) {
	digest, err := Digest(value)
	if err != nil {
		return "", err
	}
	if prefix == "" {
		return "", errors.New("stable ID prefix is required")
	}
	return prefix + "_" + digest[:24], nil
}
