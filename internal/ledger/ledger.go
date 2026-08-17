package ledger

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/domain"
)

const SchemaVersion = "receipt-ledger-v1"

type Event struct {
	Sequence     int             `json:"sequence"`
	PreviousHash string          `json:"previous_hash,omitempty"`
	Hash         string          `json:"hash"`
	Type         string          `json:"type"`
	RecordedAt   time.Time       `json:"recorded_at"`
	Payload      json.RawMessage `json:"payload"`
}

type chainInput struct {
	Sequence     int             `json:"sequence"`
	PreviousHash string          `json:"previous_hash,omitempty"`
	Type         string          `json:"type"`
	RecordedAt   time.Time       `json:"recorded_at"`
	Payload      json.RawMessage `json:"payload"`
}

type Ledger struct {
	Path string
	mu   sync.Mutex
}

func (l *Ledger) Append(eventType string, payload any, at time.Time) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if eventType == "" || at.IsZero() {
		return Event{}, errors.New("event type and timestamp are required")
	}
	events, err := readAndVerify(l.Path)
	if err != nil {
		return Event{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal ledger payload: %w", err)
	}
	event := Event{Sequence: len(events) + 1, Type: eventType, RecordedAt: at.UTC(), Payload: raw}
	if len(events) > 0 {
		event.PreviousHash = events[len(events)-1].Hash
	}
	event.Hash, err = eventHash(event)
	if err != nil {
		return Event{}, err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return Event{}, fmt.Errorf("create ledger directory: %w", err)
	}
	file, err := os.OpenFile(l.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Event{}, fmt.Errorf("open ledger: %w", err)
	}
	defer file.Close()
	encoded, err := json.Marshal(event)
	if err != nil {
		return Event{}, err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return Event{}, fmt.Errorf("append ledger: %w", err)
	}
	if err := file.Sync(); err != nil {
		return Event{}, fmt.Errorf("sync ledger: %w", err)
	}
	return event, nil
}

func Verify(path string) ([]Event, error) {
	return readAndVerify(path)
}

func readAndVerify(path string) ([]Event, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	defer file.Close()
	events := []Event{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode ledger event %d: %w", len(events)+1, err)
		}
		if event.Sequence != len(events)+1 {
			return nil, fmt.Errorf("invalid ledger sequence %d", event.Sequence)
		}
		previous := ""
		if len(events) > 0 {
			previous = events[len(events)-1].Hash
		}
		if event.PreviousHash != previous {
			return nil, fmt.Errorf("broken ledger link at sequence %d", event.Sequence)
		}
		expected, err := eventHash(event)
		if err != nil {
			return nil, err
		}
		if event.Hash != expected {
			return nil, fmt.Errorf("ledger hash mismatch at sequence %d", event.Sequence)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ledger: %w", err)
	}
	return events, nil
}

func eventHash(event Event) (string, error) {
	data, err := json.Marshal(chainInput{Sequence: event.Sequence, PreviousHash: event.PreviousHash, Type: event.Type, RecordedAt: event.RecordedAt, Payload: event.Payload})
	if err != nil {
		return "", fmt.Errorf("marshal ledger hash input: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func Reconstruct(path string) (domain.Receipt, error) {
	events, err := Verify(path)
	if err != nil {
		return domain.Receipt{}, err
	}
	var receipt domain.Receipt
	for _, event := range events {
		switch event.Type {
		case "release":
			err = json.Unmarshal(event.Payload, &receipt.Release)
		case "plan":
			err = json.Unmarshal(event.Payload, &receipt.Plan)
		case "observation":
			var observation domain.Observation
			err = json.Unmarshal(event.Payload, &observation)
			receipt.Checks = append(receipt.Checks, observation)
		case "final_verdict":
			err = json.Unmarshal(event.Payload, &receipt.Verdict)
			receipt.Generated = event.RecordedAt
		}
		if err != nil {
			return domain.Receipt{}, fmt.Errorf("decode %s event: %w", event.Type, err)
		}
	}
	if receipt.Release.ID == "" || receipt.Plan.ID == "" || receipt.Verdict.State == "" {
		return domain.Receipt{}, errors.New("ledger does not contain a complete receipt")
	}
	receipt.Schema = SchemaVersion
	return receipt, nil
}
