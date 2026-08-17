package corpus

import (
	"testing"

	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

func TestCorpusShapeAndCoverage(t *testing.T) {
	corpus := Load()
	if err := corpus.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Services) != 8 || len(corpus.Scenarios) != 25 {
		t.Fatalf("services=%d scenarios=%d", len(corpus.Services), len(corpus.Scenarios))
	}
	kinds := map[domain.ChangeKind]bool{}
	states := map[domain.HealthState]bool{}
	delayed := 0
	for _, scenario := range corpus.Scenarios {
		kinds[scenario.Release.Changed[0].Kind] = true
		states[scenario.ExpectedVerdict] = true
		if scenario.FailureStart > 0 {
			delayed++
		}
	}
	if len(kinds) != 5 || len(states) < 5 || delayed < 3 {
		t.Fatalf("kinds=%d states=%d delayed=%d", len(kinds), len(states), delayed)
	}
}

func TestCorpusIsDeterministic(t *testing.T) {
	first, _ := domain.Digest(Load())
	second, _ := domain.Digest(Load())
	if first != second {
		t.Fatalf("corpus digest changed: %s != %s", first, second)
	}
}
