package cli

import (
	"bytes"
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
