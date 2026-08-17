package domain

import (
	"testing"
	"time"
)

func TestDigestAndStableIDAreDeterministic(t *testing.T) {
	value := struct {
		A string `json:"a"`
		B int    `json:"b"`
	}{"value", 7}
	first, err := StableID("release", value)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := StableID("release", value)
	if first != second || len(first) != len("release_")+24 {
		t.Fatalf("unstable IDs: %q %q", first, second)
	}
}

func TestReleaseValidation(t *testing.T) {
	release := ReleaseEnvelope{ID: "rel-1", Repository: "fictional/checkout", CommitSHA: "abc", Environment: "production", DeployedAt: time.Now(), Changed: []ChangedItem{{Path: "handler.go", Kind: ChangeApplication, EvidenceID: "ev-1"}}, Evidence: []EvidenceRef{{ID: "ev-1"}}}
	if err := release.Validate(); err != nil {
		t.Fatal(err)
	}
	release.Changed[0].EvidenceID = ""
	if err := release.Validate(); err == nil {
		t.Fatal("expected missing evidence validation error")
	}
}
