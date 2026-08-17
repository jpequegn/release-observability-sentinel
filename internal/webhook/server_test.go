package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

func testServer(t *testing.T) (*Server, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	server, err := New(Config{BearerToken: "test-token", Now: func() time.Time { return now }, LedgerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return server, now
}

func request(t *testing.T, server http.Handler, now time.Time, path, key, nonce string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("X-Sentinel-Timestamp", now.Format(time.RFC3339))
	req.Header.Set("X-Sentinel-Nonce", nonce)
	req.Header.Set("X-Idempotency-Key", key)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	return response
}

func decodePlanID(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var output struct {
		Plan domain.WatchPlan `json:"plan"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	return output.Plan.ID
}

func scenario(t *testing.T, class string) corpus.Scenario {
	t.Helper()
	for _, item := range corpus.Load().Scenarios {
		if item.Class == class {
			return item
		}
	}
	t.Fatalf("missing scenario %s", class)
	return corpus.Scenario{}
}

func TestIntakeIsAuthenticatedAndIdempotent(t *testing.T) {
	server, now := testServer(t)
	release := scenario(t, "normal").Release
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/releases", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	first := request(t, server, now, "/releases", "release-key", "nonce-1", release)
	second := request(t, server, now, "/releases", "release-key", "nonce-1", release)
	if first.Code != http.StatusAccepted || second.Code != first.Code || second.Body.String() != first.Body.String() {
		t.Fatalf("first=%d second=%d", first.Code, second.Code)
	}
	if len(server.Deliveries()) != 1 || server.Deliveries()[0].Kind != "proposed_plan" {
		t.Fatalf("deliveries=%#v", server.Deliveries())
	}
}

func TestReplayNonceWithDifferentKeyIsRejected(t *testing.T) {
	server, now := testServer(t)
	release := scenario(t, "normal").Release
	request(t, server, now, "/releases", "key-1", "same-nonce", release)
	replayed := request(t, server, now, "/releases", "key-2", "same-nonce", release)
	if replayed.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", replayed.Code, replayed.Body.String())
	}
}

func TestEditThenApproveExecutesHealthyWatch(t *testing.T) {
	server, now := testServer(t)
	release := scenario(t, "normal").Release
	intake := request(t, server, now, "/releases", "release", "nonce-release", release)
	planID := decodePlanID(t, intake)
	edit := request(t, server, now, "/plans/"+planID+"/actions", "edit", "nonce-edit", actionRequest{Action: "edit", Actor: "operator", Reason: "short watch", CheckOffsets: []string{"0s", "15m", "30m"}})
	if edit.Code != http.StatusOK {
		t.Fatalf("edit=%d body=%s", edit.Code, edit.Body.String())
	}
	if _, ok := server.Receipt(planID); ok {
		t.Fatal("editing must not execute a watch")
	}
	approve := request(t, server, now, "/plans/"+planID+"/actions", "approve", "nonce-approve", actionRequest{Action: "approve", Actor: "operator", Reason: "bounded and relevant"})
	if approve.Code != http.StatusOK {
		t.Fatalf("approve=%d body=%s", approve.Code, approve.Body.String())
	}
	receipt, ok := server.Receipt(planID)
	if !ok || receipt.Verdict.State != domain.HealthHealthy {
		t.Fatalf("receipt=%#v ok=%v", receipt, ok)
	}
	deliveryCount := len(server.Deliveries())
	secondApproval := request(t, server, now, "/plans/"+planID+"/actions", "approve-again", "nonce-approve-again", actionRequest{Action: "approve", Actor: "operator", Reason: "retry with a different key"})
	if secondApproval.Code != http.StatusOK || len(server.Deliveries()) != deliveryCount {
		t.Fatalf("second approval status=%d deliveries=%d want=%d", secondApproval.Code, len(server.Deliveries()), deliveryCount)
	}
	for _, delivery := range server.Deliveries() {
		if delivery.Kind == "material_checkpoint" || delivery.Kind == "escalation" {
			t.Fatalf("healthy watch emitted noisy delivery: %#v", delivery)
		}
	}
}

func TestCancellationPreventsExecution(t *testing.T) {
	server, now := testServer(t)
	release := scenario(t, "normal").Release
	planID := decodePlanID(t, request(t, server, now, "/releases", "release", "nonce-release", release))
	cancel := request(t, server, now, "/plans/"+planID+"/actions", "cancel", "nonce-cancel", actionRequest{Action: "cancel", Actor: "operator", Reason: "deployment reverted"})
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel=%d body=%s", cancel.Code, cancel.Body.String())
	}
	plan, _ := server.Plan(planID)
	if plan.Approval.State != domain.ApprovalCancelled {
		t.Fatalf("approval=%s", plan.Approval.State)
	}
	if _, ok := server.Receipt(planID); ok {
		t.Fatal("cancelled plan must not execute")
	}
}

func TestUnhealthyWatchRoutesMaterialEscalationToOwner(t *testing.T) {
	server, now := testServer(t)
	release := scenario(t, "immediate").Release
	planID := decodePlanID(t, request(t, server, now, "/releases", "release", "nonce-release", release))
	approve := request(t, server, now, "/plans/"+planID+"/actions", "approve", "nonce-approve", actionRequest{Action: "approve", Actor: "operator", Reason: "execute"})
	if approve.Code != http.StatusOK {
		t.Fatalf("approve=%d body=%s", approve.Code, approve.Body.String())
	}
	wanted := map[string]bool{"material_checkpoint": false, "final_report": false, "escalation": false}
	for _, delivery := range server.Deliveries() {
		if _, ok := wanted[delivery.Kind]; ok {
			wanted[delivery.Kind] = delivery.Owner == release.Owner && delivery.State == domain.HealthUnhealthy
		}
	}
	for kind, valid := range wanted {
		if !valid {
			t.Fatalf("missing owner-routed %s in %#v", kind, server.Deliveries())
		}
	}
}
