package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
	"github.com/jpequegn/release-observability-sentinel/internal/ledger"
	"github.com/jpequegn/release-observability-sentinel/internal/mapping"
	"github.com/jpequegn/release-observability-sentinel/internal/planning"
	"github.com/jpequegn/release-observability-sentinel/internal/telemetry"
	"github.com/jpequegn/release-observability-sentinel/internal/watch"
)

type Config struct {
	BearerToken  string
	ReplayWindow time.Duration
	LedgerDir    string
	Now          func() time.Time
}

type Delivery struct {
	Kind      string             `json:"kind"`
	PlanID    string             `json:"plan_id"`
	Owner     string             `json:"owner"`
	State     domain.HealthState `json:"state,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
}

type cachedResponse struct {
	status int
	body   []byte
}

type Server struct {
	config      Config
	corpus      corpus.Corpus
	mu          sync.Mutex
	nonces      map[string]time.Time
	idempotency map[string]cachedResponse
	releases    map[string]domain.ReleaseEnvelope
	plans       map[string]domain.WatchPlan
	scenarios   map[string]corpus.Scenario
	receipts    map[string]domain.Receipt
	deliveries  []Delivery
}

func New(config Config) (*Server, error) {
	if config.BearerToken == "" {
		return nil, errors.New("webhook bearer token is required")
	}
	if config.ReplayWindow <= 0 {
		config.ReplayWindow = 5 * time.Minute
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	data := corpus.Load()
	if err := data.Validate(); err != nil {
		return nil, err
	}
	return &Server{config: config, corpus: data, nonces: map[string]time.Time{}, idempotency: map[string]cachedResponse{}, releases: map[string]domain.ReleaseEnvelope{}, plans: map[string]domain.WatchPlan{}, scenarios: map[string]corpus.Scenario{}, receipts: map[string]domain.Receipt{}}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost && request.URL.Path == "/releases" {
		s.mutate(w, request, s.intake)
		return
	}
	if request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/plans/") && strings.HasSuffix(request.URL.Path, "/actions") {
		s.mutate(w, request, s.action)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
}

func (s *Server) mutate(w http.ResponseWriter, request *http.Request, handler func(*http.Request) (int, any, error)) {
	key := request.Header.Get("X-Idempotency-Key")
	if err := s.authenticate(request); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "X-Idempotency-Key is required"})
		return
	}
	cacheKey := request.Method + " " + request.URL.Path + " " + key
	s.mu.Lock()
	if cached, ok := s.idempotency[cacheKey]; ok {
		s.mu.Unlock()
		writeRawJSON(w, cached.status, cached.body)
		return
	}
	nonce := request.Header.Get("X-Sentinel-Nonce")
	if _, used := s.nonces[nonce]; used {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "replayed nonce"})
		return
	}
	s.nonces[nonce] = s.config.Now().UTC()
	s.mu.Unlock()

	status, response, err := handler(request)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	body, err := json.Marshal(response)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode response"})
		return
	}
	s.mu.Lock()
	s.idempotency[cacheKey] = cachedResponse{status: status, body: body}
	s.mu.Unlock()
	writeRawJSON(w, status, body)
}

func (s *Server) authenticate(request *http.Request) error {
	if request.Header.Get("Authorization") != "Bearer "+s.config.BearerToken {
		return errors.New("invalid bearer token")
	}
	timestamp, err := time.Parse(time.RFC3339, request.Header.Get("X-Sentinel-Timestamp"))
	if err != nil {
		return errors.New("invalid replay timestamp")
	}
	delta := s.config.Now().UTC().Sub(timestamp.UTC())
	if delta < 0 {
		delta = -delta
	}
	if delta > s.config.ReplayWindow {
		return errors.New("request is outside the replay window")
	}
	if request.Header.Get("X-Sentinel-Nonce") == "" {
		return errors.New("X-Sentinel-Nonce is required")
	}
	return nil
}

func (s *Server) intake(request *http.Request) (int, any, error) {
	var release domain.ReleaseEnvelope
	if err := json.NewDecoder(request.Body).Decode(&release); err != nil {
		return http.StatusBadRequest, nil, fmt.Errorf("decode release: %w", err)
	}
	if err := release.Validate(); err != nil {
		return http.StatusBadRequest, nil, err
	}
	scenario, ok := s.scenarioByRelease(release.ID)
	if !ok {
		return http.StatusUnprocessableEntity, nil, errors.New("simulator only accepts releases from the fictional corpus")
	}
	packets := contextPackets(s.corpus.Services, release.DeployedAt)
	mapped, err := mapping.New(s.corpus.Services, nil).Map(request.Context(), release, packets)
	if err != nil {
		return http.StatusUnprocessableEntity, nil, err
	}
	plan, decision, err := planning.New(planning.DefaultLimits()).Build(release, mapped)
	if err != nil {
		return http.StatusUnprocessableEntity, nil, err
	}
	if !decision.Allowed {
		return http.StatusUnprocessableEntity, nil, errors.New("proposed plan failed policy")
	}
	s.mu.Lock()
	s.releases[release.ID] = release
	s.plans[plan.ID] = plan
	s.scenarios[plan.ID] = scenario
	s.deliveries = append(s.deliveries, Delivery{Kind: "proposed_plan", PlanID: plan.ID, Owner: plan.Owner, CreatedAt: s.config.Now().UTC()})
	s.mu.Unlock()
	return http.StatusAccepted, map[string]any{"release_id": release.ID, "plan": plan}, nil
}

type actionRequest struct {
	Action       string   `json:"action"`
	Actor        string   `json:"actor"`
	Reason       string   `json:"reason"`
	CheckOffsets []string `json:"check_offsets,omitempty"`
}

func (s *Server) action(request *http.Request) (int, any, error) {
	planID, ok := planIDFromPath(request.URL.Path)
	if !ok {
		return http.StatusNotFound, nil, errors.New("invalid plan action path")
	}
	var input actionRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		return http.StatusBadRequest, nil, fmt.Errorf("decode action: %w", err)
	}
	s.mu.Lock()
	plan, exists := s.plans[planID]
	release := s.releases[plan.ReleaseID]
	scenario := s.scenarios[planID]
	existingReceipt, alreadyExecuted := s.receipts[planID]
	s.mu.Unlock()
	if !exists {
		return http.StatusNotFound, nil, errors.New("plan not found")
	}
	at := s.config.Now().UTC()
	switch input.Action {
	case "edit":
		offsets, err := parseOffsets(input.CheckOffsets)
		if err != nil {
			return http.StatusBadRequest, nil, err
		}
		plan.CheckOffsets = offsets
		plan, err = planning.Transition(plan, domain.ApprovalEdited, input.Actor, input.Reason, at, planning.DefaultLimits())
		if err != nil {
			return http.StatusUnprocessableEntity, nil, err
		}
		s.storePlanAndDelivery(plan, Delivery{Kind: "edited_plan", PlanID: plan.ID, Owner: plan.Owner, CreatedAt: at})
		return http.StatusOK, map[string]any{"plan": plan}, nil
	case "cancel":
		var err error
		plan, err = planning.Transition(plan, domain.ApprovalCancelled, input.Actor, input.Reason, at, planning.DefaultLimits())
		if err != nil {
			return http.StatusUnprocessableEntity, nil, err
		}
		s.storePlanAndDelivery(plan, Delivery{Kind: "cancelled", PlanID: plan.ID, Owner: plan.Owner, CreatedAt: at})
		return http.StatusOK, map[string]any{"plan": plan}, nil
	case "approve":
		if alreadyExecuted {
			return http.StatusOK, map[string]any{"plan": plan, "receipt": existingReceipt}, nil
		}
		var err error
		plan, err = planning.Transition(plan, domain.ApprovalApproved, input.Actor, input.Reason, at, planning.DefaultLimits())
		if err != nil {
			return http.StatusUnprocessableEntity, nil, err
		}
		s.mu.Lock()
		s.plans[plan.ID] = plan
		s.mu.Unlock()
		receipt, err := s.run(request.Context(), scenario, release, plan)
		if err != nil {
			return http.StatusInternalServerError, nil, err
		}
		s.mu.Lock()
		s.receipts[plan.ID] = receipt
		if receipt.Verdict.Escalate {
			s.deliveries = append(s.deliveries, Delivery{Kind: "material_checkpoint", PlanID: plan.ID, Owner: plan.Owner, State: receipt.Verdict.State, CreatedAt: receipt.Generated})
		}
		s.deliveries = append(s.deliveries, Delivery{Kind: "final_report", PlanID: plan.ID, Owner: plan.Owner, State: receipt.Verdict.State, CreatedAt: receipt.Generated})
		if receipt.Verdict.Escalate {
			s.deliveries = append(s.deliveries, Delivery{Kind: "escalation", PlanID: plan.ID, Owner: plan.Owner, State: receipt.Verdict.State, CreatedAt: receipt.Generated})
		}
		s.mu.Unlock()
		return http.StatusOK, map[string]any{"plan": plan, "receipt": receipt}, nil
	default:
		return http.StatusBadRequest, nil, errors.New("action must be edit, approve, or cancel")
	}
}

func (s *Server) run(ctx context.Context, scenario corpus.Scenario, release domain.ReleaseEnvelope, plan domain.WatchPlan) (domain.Receipt, error) {
	adapters := map[domain.Backend]telemetry.Adapter{}
	for _, backend := range []domain.Backend{domain.BackendPrometheus, domain.BackendTempo, domain.BackendLoki} {
		adapters[backend] = telemetry.FixtureAdapter{Kind: backend, Scenario: scenario}
	}
	clock := watch.NewVirtualClock(release.DeployedAt)
	var receiptLedger *ledger.Ledger
	if s.config.LedgerDir != "" {
		receiptLedger = &ledger.Ledger{Path: filepath.Join(s.config.LedgerDir, plan.ID+".jsonl")}
	}
	runner := watch.Runner{Executor: telemetry.Executor{Adapters: adapters}, Clock: clock, Ledger: receiptLedger}
	return runner.Run(ctx, release, plan)
}

func (s *Server) storePlanAndDelivery(plan domain.WatchPlan, delivery Delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[plan.ID] = plan
	s.deliveries = append(s.deliveries, delivery)
}

func (s *Server) scenarioByRelease(releaseID string) (corpus.Scenario, bool) {
	for _, scenario := range s.corpus.Scenarios {
		if scenario.Release.ID == releaseID {
			return scenario, true
		}
	}
	return corpus.Scenario{}, false
}

func contextPackets(services []domain.ServiceContext, at time.Time) []mapping.ContextPacket {
	packets := make([]mapping.ContextPacket, 0, len(services))
	for _, service := range services {
		packets = append(packets, mapping.ContextPacket{Service: service.ID, QuestionTime: at, State: "current", Facts: []mapping.ContextFact{{FactID: "fact-" + service.ID, Service: service.ID, Predicate: "uses_metric", Value: service.Signals[domain.BackendPrometheus][0], State: "current", Authority: 100, RecordedAt: at.Add(-time.Minute), ValidFrom: service.RecordedAt, Source: service.Evidence}}})
	}
	return packets
}

func parseOffsets(values []string) ([]time.Duration, error) {
	if len(values) == 0 {
		return nil, errors.New("edited plan requires check_offsets")
	}
	offsets := make([]time.Duration, 0, len(values))
	for _, value := range values {
		offset, err := time.ParseDuration(value)
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid check offset %q", value)
		}
		offsets = append(offsets, offset)
	}
	return offsets, nil
}

func planIDFromPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	returnValue := ""
	if len(parts) == 3 && parts[0] == "plans" && parts[2] == "actions" {
		returnValue = parts[1]
	}
	return returnValue, returnValue != ""
}

func (s *Server) Deliveries() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery(nil), s.deliveries...)
}

func (s *Server) Plan(id string) (domain.WatchPlan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[id]
	return plan, ok
}

func (s *Server) Receipt(id string) (domain.Receipt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[id]
	return receipt, ok
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	writeRawJSON(w, status, body)
}

func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
