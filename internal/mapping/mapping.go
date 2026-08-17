package mapping

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

type ContextFact struct {
	FactID     string             `json:"fact_id"`
	Service    string             `json:"service"`
	Predicate  string             `json:"predicate"`
	Value      string             `json:"value"`
	State      string             `json:"state"`
	Authority  int                `json:"authority"`
	RecordedAt time.Time          `json:"recorded_at"`
	ValidFrom  time.Time          `json:"valid_from"`
	ValidTo    *time.Time         `json:"valid_to,omitempty"`
	Source     domain.EvidenceRef `json:"source"`
}

type ContextPacket struct {
	Service      string        `json:"service"`
	QuestionTime time.Time     `json:"question_time"`
	State        string        `json:"state"`
	Facts        []ContextFact `json:"facts"`
	Warnings     []string      `json:"warnings,omitempty"`
}

type MappedService struct {
	Service         domain.ServiceContext       `json:"service"`
	Reasons         []string                    `json:"reasons"`
	ReleaseEvidence []domain.EvidenceRef        `json:"release_evidence"`
	ContextEvidence []domain.EvidenceRef        `json:"context_evidence"`
	Signals         map[domain.Backend][]string `json:"signals"`
	MissingContext  []string                    `json:"missing_context,omitempty"`
	Explanation     string                      `json:"explanation,omitempty"`
}

type Result struct {
	ReleaseID string               `json:"release_id"`
	Services  []MappedService      `json:"services"`
	Unknown   []domain.ChangedItem `json:"unknown_changes"`
}

type Explainer interface {
	Explain(context.Context, domain.ReleaseEnvelope, domain.ServiceContext, []string) (string, error)
}

type Mapper struct {
	services  map[string]domain.ServiceContext
	explainer Explainer
}

func New(services []domain.ServiceContext, explainer Explainer) *Mapper {
	indexed := make(map[string]domain.ServiceContext, len(services))
	for _, service := range services {
		indexed[service.ID] = service
	}
	return &Mapper{services: indexed, explainer: explainer}
}

func (m *Mapper) Map(ctx context.Context, release domain.ReleaseEnvelope, packets []ContextPacket) (Result, error) {
	if err := release.Validate(); err != nil {
		return Result{}, err
	}
	packetByService := map[string]ContextPacket{}
	for _, packet := range packets {
		if packet.QuestionTime.After(release.DeployedAt) {
			continue
		}
		packetByService[packet.Service] = packet
	}
	reasons := map[string][]string{}
	evidence := map[string]map[string]domain.EvidenceRef{}
	unknown := []domain.ChangedItem{}
	for _, changed := range release.Changed {
		serviceID, ok := m.serviceFromPath(changed.Path)
		if !ok {
			unknown = append(unknown, changed)
			continue
		}
		affected := []string{serviceID}
		if changed.Kind == domain.ChangeDependency || changed.Kind == domain.ChangeSchema {
			affected = append(affected, m.downstream(serviceID)...)
		}
		for _, id := range unique(affected) {
			reasons[id] = append(reasons[id], fmt.Sprintf("%s change at %s", changed.Kind, changed.Path))
			if evidence[id] == nil {
				evidence[id] = map[string]domain.EvidenceRef{}
			}
			if ref, ok := evidenceByID(release.Evidence, changed.EvidenceID); ok {
				evidence[id][ref.ID] = ref
			}
		}
	}
	ids := make([]string, 0, len(reasons))
	for id := range reasons {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	mapped := make([]MappedService, 0, len(ids))
	for _, id := range ids {
		service := m.services[id]
		item := MappedService{Service: service, Reasons: reasons[id], Signals: cloneSignals(service.Signals)}
		for _, ref := range evidence[id] {
			item.ReleaseEvidence = append(item.ReleaseEvidence, ref)
		}
		sort.Slice(item.ReleaseEvidence, func(i, j int) bool { return item.ReleaseEvidence[i].ID < item.ReleaseEvidence[j].ID })
		if packet, ok := packetByService[id]; ok && packet.State == "current" {
			for _, fact := range packet.Facts {
				if fact.RecordedAt.After(release.DeployedAt) {
					continue
				}
				item.ContextEvidence = append(item.ContextEvidence, fact.Source)
				if fact.Predicate == "uses_metric" {
					item.Signals[domain.BackendPrometheus] = unique(append(item.Signals[domain.BackendPrometheus], fact.Value))
				}
			}
		} else {
			item.MissingContext = append(item.MissingContext, "current temporal context packet")
		}
		if m.explainer != nil {
			explanation, err := m.explainer.Explain(ctx, release, service, item.Reasons)
			if err != nil {
				return Result{}, fmt.Errorf("explain mapping for %s: %w", id, err)
			}
			item.Explanation = explanation
		}
		mapped = append(mapped, item)
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i].Path < unknown[j].Path })
	return Result{ReleaseID: release.ID, Services: mapped, Unknown: unknown}, nil
}

func (m *Mapper) serviceFromPath(path string) (string, bool) {
	for id := range m.services {
		patterns := []string{"services/" + id + "/", "config/flags/" + id + ".", "migrations/" + id + "/", "infra/" + id + "/"}
		for _, pattern := range patterns {
			if strings.HasPrefix(path, pattern) {
				return id, true
			}
		}
	}
	return "", false
}

func (m *Mapper) downstream(serviceID string) []string {
	reverse := map[string][]string{}
	for _, service := range m.services {
		for _, dependency := range service.Dependencies {
			reverse[dependency] = append(reverse[dependency], service.ID)
		}
	}
	seen := map[string]bool{serviceID: true}
	queue := []string{serviceID}
	result := []string{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependent := range reverse[current] {
			if !seen[dependent] {
				seen[dependent] = true
				result = append(result, dependent)
				queue = append(queue, dependent)
			}
		}
	}
	sort.Strings(result)
	return result
}

func evidenceByID(refs []domain.EvidenceRef, id string) (domain.EvidenceRef, bool) {
	for _, ref := range refs {
		if ref.ID == id {
			return ref, true
		}
	}
	return domain.EvidenceRef{}, false
}

func cloneSignals(input map[domain.Backend][]string) map[domain.Backend][]string {
	output := make(map[domain.Backend][]string, len(input))
	for backend, signals := range input {
		output[backend] = append([]string(nil), signals...)
	}
	return output
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
