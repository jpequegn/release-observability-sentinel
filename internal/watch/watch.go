package watch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jpequegn/release-observability-sentinel/internal/domain"
	"github.com/jpequegn/release-observability-sentinel/internal/ledger"
	"github.com/jpequegn/release-observability-sentinel/internal/telemetry"
)

type Clock interface {
	Now() time.Time
	WaitUntil(context.Context, time.Time) error
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

func (RealClock) WaitUntil(ctx context.Context, target time.Time) error {
	delay := time.Until(target)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type VirtualClock struct {
	mu      sync.Mutex
	current time.Time
	waits   []time.Time
}

func NewVirtualClock(start time.Time) *VirtualClock {
	return &VirtualClock{current: start.UTC()}
}

func (c *VirtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *VirtualClock) WaitUntil(ctx context.Context, target time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	target = target.UTC()
	c.waits = append(c.waits, target)
	if target.After(c.current) {
		c.current = target
	}
	return nil
}

func (c *VirtualClock) Waits() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.waits...)
}

type Runner struct {
	Executor telemetry.Executor
	Clock    Clock
	Ledger   *ledger.Ledger
}

func (r Runner) Run(ctx context.Context, release domain.ReleaseEnvelope, plan domain.WatchPlan) (domain.Receipt, error) {
	if plan.Approval.State != domain.ApprovalApproved {
		return domain.Receipt{}, fmt.Errorf("watch plan must be approved, got %s", plan.Approval.State)
	}
	if plan.ReleaseID != release.ID {
		return domain.Receipt{}, errors.New("watch plan release does not match release envelope")
	}
	clock := r.Clock
	if clock == nil {
		clock = RealClock{}
	}
	if err := appendEvent(r.Ledger, "release", release, clock.Now()); err != nil {
		return domain.Receipt{}, err
	}
	if err := appendEvent(r.Ledger, "plan", plan, clock.Now()); err != nil {
		return domain.Receipt{}, err
	}
	offsets := append([]time.Duration(nil), plan.CheckOffsets...)
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	checks := []domain.Observation{}
	checkpoints := []domain.Verdict{}
	lastRecorded := domain.HealthState("")
	for _, offset := range offsets {
		if offset < 0 || offset > plan.MaxDuration {
			return domain.Receipt{}, fmt.Errorf("check offset %s is outside plan duration", offset)
		}
		target := release.DeployedAt.Add(offset)
		if err := clock.WaitUntil(ctx, target); err != nil {
			return domain.Receipt{}, fmt.Errorf("wait for checkpoint: %w", err)
		}
		executor := r.Executor
		executor.Now = clock.Now
		batch := make([]domain.Observation, 0, len(plan.Queries))
		for _, query := range plan.Queries {
			observation := executor.Execute(ctx, query, target)
			batch = append(batch, observation)
			checks = append(checks, observation)
			if err := appendEvent(r.Ledger, "observation", observation, clock.Now()); err != nil {
				return domain.Receipt{}, err
			}
		}
		checkpoint := telemetry.Grade(batch, clock.Now())
		checkpoints = append(checkpoints, checkpoint)
		if checkpoint.State != domain.HealthHealthy || checkpoint.State != lastRecorded {
			if err := appendEvent(r.Ledger, "checkpoint_verdict", checkpoint, clock.Now()); err != nil {
				return domain.Receipt{}, err
			}
			lastRecorded = checkpoint.State
		}
		if checkpoint.State == domain.HealthUnhealthy || checkpoint.State == domain.HealthQueryFailure || checkpoint.State == domain.HealthMissingTelemetry {
			break
		}
	}
	final := aggregate(checkpoints, clock.Now())
	receipt := domain.Receipt{Release: release, Plan: plan, Checks: checks, Verdict: final, Generated: clock.Now(), Schema: ledger.SchemaVersion}
	if err := appendEvent(r.Ledger, "final_verdict", final, clock.Now()); err != nil {
		return domain.Receipt{}, err
	}
	return receipt, nil
}

func aggregate(checkpoints []domain.Verdict, at time.Time) domain.Verdict {
	if len(checkpoints) == 0 {
		return telemetry.Grade(nil, at)
	}
	ids := []string{}
	seen := map[domain.HealthState]bool{}
	for _, checkpoint := range checkpoints {
		seen[checkpoint.State] = true
		ids = append(ids, checkpoint.ObservationIDs...)
	}
	result := domain.Verdict{ObservationIDs: ids, GraderVersion: telemetry.GraderVersion, EvaluatedAt: at}
	switch {
	case seen[domain.HealthQueryFailure]:
		result.State, result.Reason, result.Escalate = domain.HealthQueryFailure, "a scheduled telemetry query failed", true
	case seen[domain.HealthMissingTelemetry]:
		result.State, result.Reason, result.Escalate = domain.HealthMissingTelemetry, "scheduled telemetry was missing", true
	case seen[domain.HealthUnhealthy]:
		result.State, result.Reason, result.Escalate = domain.HealthUnhealthy, "a scheduled checkpoint violated a deterministic invariant", true
	case seen[domain.HealthAmbiguous]:
		result.State, result.Reason, result.Escalate = domain.HealthAmbiguous, "one or more scheduled checkpoints were ambiguous", true
	case seen[domain.HealthHealthy]:
		result.State, result.Reason = domain.HealthHealthy, "all scheduled checkpoints were healthy"
	default:
		result.State, result.Reason, result.Escalate = domain.HealthInsufficientEvidence, "scheduled checks produced insufficient evidence", true
	}
	return result
}

func appendEvent(log *ledger.Ledger, kind string, payload any, at time.Time) error {
	if log == nil {
		return nil
	}
	_, err := log.Append(kind, payload, at)
	return err
}

func RenderReport(receipt domain.Receipt) string {
	var report strings.Builder
	fmt.Fprintf(&report, "# Release health report: %s\n\n", receipt.Release.ID)
	fmt.Fprintf(&report, "- Status: **%s**\n", receipt.Verdict.State)
	fmt.Fprintf(&report, "- Owner: %s\n", receipt.Plan.Owner)
	fmt.Fprintf(&report, "- Plan: `%s`\n", receipt.Plan.ID)
	fmt.Fprintf(&report, "- Approval: %s by %s\n", receipt.Plan.Approval.State, receipt.Plan.Approval.Actor)
	fmt.Fprintf(&report, "- Evidence observations: %d\n", len(receipt.Checks))
	fmt.Fprintf(&report, "- Evaluated at: %s\n\n", receipt.Verdict.EvaluatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&report, "## Assessment\n\n%s\n", receipt.Verdict.Reason)
	if receipt.Verdict.Escalate {
		fmt.Fprintf(&report, "\nEscalation required for `%s`; rollback reference: `%s`.\n", receipt.Release.Owner, receipt.Release.RollbackRef)
	}
	return report.String()
}
