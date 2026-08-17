package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jpequegn/release-observability-sentinel/internal/version"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != version.Current {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCorpusValidateAndPlanPreview(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"corpus", "validate"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "25 releases") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"plan", "preview", "--scenario", "scenario-003"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "\"approval\"") || !strings.Contains(stdout.String(), "\"proposed\"") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestReplayWritesReportsAndPassesGate(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "replay.json")
	markdownPath := filepath.Join(dir, "replay.md")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"replay", "--json-report", jsonPath, "--markdown-report", markdownPath}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "**TRUE**") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, path := range []string{jsonPath, markdownPath} {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Fatalf("artifact %s info=%v err=%v", path, info, err)
		}
	}
}

func TestDemoLedgerVerifyAndAttentionStatus(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"demo", "--output-dir", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("demo code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, name := range []string{"replay.json", "replay.md", "watch-plan.json", "receipt.json", "delayed-release.jsonl", "health-report.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	stdout.Reset()
	stderr.Reset()
	ledgerPath := filepath.Join(dir, "delayed-release.jsonl")
	if code := Run([]string{"ledger", "verify", "--path", ledgerPath}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "checksum-linked") {
		t.Fatalf("verify code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--ledger", ledgerPath}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "unhealthy") {
		t.Fatalf("status code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestLedgerVerifyRejectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"ledger", "verify", "--path", path}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "decode ledger") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}
