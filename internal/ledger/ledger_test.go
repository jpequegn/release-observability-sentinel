package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

func TestAppendVerifyAndReconstruct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	log := &Ledger{Path: path}
	release := domain.ReleaseEnvelope{ID: "release-1"}
	plan := domain.WatchPlan{ID: "plan-1"}
	observation := domain.Observation{ID: "obs-1"}
	verdict := domain.Verdict{State: domain.HealthHealthy, EvaluatedAt: now}
	for _, item := range []struct {
		kind    string
		payload any
	}{{"release", release}, {"plan", plan}, {"observation", observation}, {"final_verdict", verdict}} {
		if _, err := log.Append(item.kind, item.payload, now); err != nil {
			t.Fatal(err)
		}
	}
	events, err := Verify(path)
	if err != nil || len(events) != 4 || events[1].PreviousHash != events[0].Hash {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	receipt, err := Reconstruct(path)
	if err != nil || receipt.Release.ID != release.ID || len(receipt.Checks) != 1 || receipt.Schema != SchemaVersion {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	log := &Ledger{Path: path}
	if _, err := log.Append("release", map[string]string{"status": "healthy"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "healthy", "unhealthy", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected hash mismatch, got %v", err)
	}
}
