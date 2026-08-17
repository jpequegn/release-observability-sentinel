package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jpequegn/release-observability-sentinel/internal/corpus"
	"github.com/jpequegn/release-observability-sentinel/internal/domain"
	"github.com/jpequegn/release-observability-sentinel/internal/evaluation"
	"github.com/jpequegn/release-observability-sentinel/internal/ledger"
	"github.com/jpequegn/release-observability-sentinel/internal/version"
	"github.com/jpequegn/release-observability-sentinel/internal/watch"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}
	var err error
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version.Current)
		return 0
	case "help", "-h", "--help":
		printHelp(stdout)
		return 0
	case "corpus":
		err = runCorpus(args[1:], stdout)
	case "plan":
		err = runPlan(args[1:], stdout, stderr)
	case "replay":
		err = runReplay(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "ledger":
		err = runLedger(args[1:], stdout, stderr)
	case "demo":
		err = runDemo(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printHelp(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	return 0
}

func runCorpus(args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "validate" {
		return errors.New("usage: sentinel corpus validate")
	}
	data := corpus.Load()
	if err := data.Validate(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "valid corpus %s: %d releases, %d services\n", data.Version, len(data.Scenarios), len(data.Services))
	return nil
}

func runPlan(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "preview" {
		return errors.New("usage: sentinel plan preview --scenario <id>")
	}
	flags := flag.NewFlagSet("plan preview", flag.ContinueOnError)
	flags.SetOutput(stderr)
	scenarioID := flags.String("scenario", "", "corpus scenario or release ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	data := corpus.Load()
	scenario, ok := evaluation.FindScenario(data, *scenarioID)
	if !ok {
		return fmt.Errorf("scenario %q not found", *scenarioID)
	}
	plan, err := evaluation.BuildPlan(context.Background(), data, scenario)
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{"scenario": scenario.ID, "plan": plan})
}

func runReplay(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonPath := flags.String("json-report", "", "write JSON report")
	markdownPath := flags.String("markdown-report", "", "write Markdown report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report, err := evaluation.Run(context.Background())
	if err != nil {
		return err
	}
	jsonReport, err := evaluation.JSON(report)
	if err != nil {
		return err
	}
	markdown := evaluation.Markdown(report)
	if *jsonPath != "" {
		if err := writeFile(*jsonPath, append(jsonReport, '\n')); err != nil {
			return err
		}
	}
	if *markdownPath != "" {
		if err := writeFile(*markdownPath, []byte(markdown)); err != nil {
			return err
		}
	}
	fmt.Fprint(stdout, markdown)
	if !report.Gate.Passed {
		return errors.New("replay quality gate failed")
	}
	return nil
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ledgerPath := flags.String("ledger", "", "receipt ledger path")
	if err := flags.Parse(args); err != nil || *ledgerPath == "" {
		fmt.Fprintln(stderr, "error: usage: sentinel status --ledger <path>")
		return 2
	}
	receipt, err := ledger.Reconstruct(*ledgerPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: ledger requires attention:", err)
		return 2
	}
	fmt.Fprint(stdout, watch.RenderReport(receipt))
	if receipt.Verdict.State != domain.HealthHealthy || receipt.Verdict.Escalate {
		return 1
	}
	return 0
}

func runLedger(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "verify" {
		return errors.New("usage: sentinel ledger verify --path <ledger>")
	}
	flags := flag.NewFlagSet("ledger verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("path", "", "receipt ledger path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("ledger path is required")
	}
	events, err := ledger.Verify(*path)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return errors.New("ledger is empty")
	}
	fmt.Fprintf(stdout, "verified %d checksum-linked events; head=%s\n", len(events), events[len(events)-1].Hash)
	return nil
}

func runDemo(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputDir := flags.String("output-dir", "demo-output", "artifact output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		return err
	}
	report, err := evaluation.Run(context.Background())
	if err != nil {
		return err
	}
	jsonReport, err := evaluation.JSON(report)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(*outputDir, "replay.json"), append(jsonReport, '\n')); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(*outputDir, "replay.md"), []byte(evaluation.Markdown(report))); err != nil {
		return err
	}
	data := corpus.Load()
	scenario, _ := evaluation.FindScenario(data, "scenario-003")
	ledgerPath := filepath.Join(*outputDir, "delayed-release.jsonl")
	if err := os.Remove(ledgerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	plan, receipt, err := evaluation.RunScenarioWithLedger(context.Background(), data, scenario, ledgerPath)
	if err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(*outputDir, "watch-plan.json"), plan); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(*outputDir, "receipt.json"), receipt); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(*outputDir, "health-report.md"), []byte(watch.RenderReport(receipt))); err != nil {
		return err
	}
	if !report.Gate.Passed {
		return errors.New("demo replay quality gate failed")
	}
	fmt.Fprintf(stdout, "demo complete: %s\nquality gate: passed\ndelayed release: %s\n", *outputDir, receipt.Verdict.State)
	return nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'))
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "release-observability-sentinel")
	fmt.Fprintln(w, "usage: sentinel <command>")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  corpus validate")
	fmt.Fprintln(w, "  plan preview --scenario <id>")
	fmt.Fprintln(w, "  replay [--json-report <path>] [--markdown-report <path>]")
	fmt.Fprintln(w, "  status --ledger <path>")
	fmt.Fprintln(w, "  ledger verify --path <path>")
	fmt.Fprintln(w, "  demo [--output-dir <path>]")
	fmt.Fprintln(w, "  version")
}
