package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"ownward.local/governance/internal/governance"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "governance-cli:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command")
	}
	runtime, err := governance.Open("")
	if err != nil {
		// Governance is advisory. A broken or temporarily unavailable runtime
		// must never make a Codex Hook block the main task.
		if args[0] == "hook" {
			_, _ = fmt.Fprintln(os.Stdout, "{}")
			return nil
		}
		return err
	}
	switch args[0] {
	case "init":
		state, err := runtime.Init()
		return output(state, err)
	case "hook":
		if len(args) != 2 {
			return errors.New("hook requires exactly one event name")
		}
		return runtime.HandleHook(args[1], os.Stdin, os.Stdout)
	case "status":
		state, err := runtime.LoadState()
		return output(state, err)
	case "update-execution-snapshot":
		var snapshot governance.ExecutionSnapshotInput
		if err := decodeInput(args[1:], &snapshot); err != nil {
			return err
		}
		request, err := runtime.UpdateExecutionSnapshot(snapshot)
		return output(request, err)
	case "record-evidence":
		var record governance.EvidenceRecord
		if err := decodeInput(args[1:], &record); err != nil {
			return err
		}
		request, err := runtime.RecordEvidence(record)
		return output(request, err)
	case "record-failure":
		return errors.New("manual failure counters are disabled; governed hooks and governed-run record verified events automatically")
	case "record-repair":
		var repair governance.FailureRepairInput
		if err := decodeInput(args[1:], &repair); err != nil {
			return err
		}
		value, err := runtime.RecordRepair(repair)
		return output(value, err)
	case "request-advisory-review":
		parser := flag.NewFlagSet("request-advisory-review", flag.ContinueOnError)
		requestID := parser.String("request-id", "", "stable identity of this explicit advisory request")
		reason := parser.String("reason", "", "review trigger reason")
		if err := parser.Parse(args[1:]); err != nil {
			return err
		}
		request, err := runtime.RequestAdvisoryReview(*requestID, *reason)
		return output(request, err)
	case "request-completion-review":
		parser := flag.NewFlagSet("request-completion-review", flag.ContinueOnError)
		sourceID := parser.String("source-id", "", "stable identity of the explicit completion attempt")
		if err := parser.Parse(args[1:]); err != nil {
			return err
		}
		request, err := runtime.RequestCompletionReview(*sourceID)
		return output(request, err)
	case "resolve-intervention":
		var resolution governance.ResolveInterventionInput
		if err := decodeInput(args[1:], &resolution); err != nil {
			return err
		}
		request, err := runtime.ResolveIntervention(resolution)
		return output(request, err)
	case "prepare-handoff":
		parser := flag.NewFlagSet("prepare-handoff", flag.ContinueOnError)
		sessionID := parser.String("session-id", "", "current owner hook session id")
		if err := parser.Parse(args[1:]); err != nil {
			return err
		}
		ticket, err := runtime.PrepareHandoff(*sessionID)
		return output(ticket, err)
	case "bind-handoff":
		parser := flag.NewFlagSet("bind-handoff", flag.ContinueOnError)
		handoffID := parser.String("handoff-id", "", "prepared handoff id")
		targetThreadID := parser.String("target-thread-id", "", "thread id returned by Codex fork")
		if err := parser.Parse(args[1:]); err != nil {
			return err
		}
		return output(map[string]string{"status": "bound"}, runtime.BindHandoff(*handoffID, *targetThreadID))
	case "cancel-handoff":
		parser := flag.NewFlagSet("cancel-handoff", flag.ContinueOnError)
		handoffID := parser.String("handoff-id", "", "prepared handoff id")
		if err := parser.Parse(args[1:]); err != nil {
			return err
		}
		return output(map[string]string{"status": "cancelled"}, runtime.CancelHandoff(*handoffID))
	case "accept-review":
		var result governance.ReviewResult
		if err := decodeInput(args[1:], &result); err != nil {
			return err
		}
		path, err := runtime.AcceptReview(result)
		return output(map[string]any{"review_path": path}, err)
	case "record-review-response":
		var response governance.ReviewResponseInput
		if err := decodeInput(args[1:], &response); err != nil {
			return err
		}
		state, err := runtime.RecordReviewResponse(response)
		return output(state, err)
	case "apply-review":
		state, err := runtime.ApplyReviewCompatibility()
		return output(state, err)
	case "complete-execution-snapshot":
		err := runtime.CompleteExecutionSnapshot()
		return output(map[string]string{"status": "closed"}, err)
	case "finish":
		err := runtime.Finish()
		return output(map[string]string{"status": "complete"}, err)
	case "doctor":
		report, err := runtime.Doctor()
		return output(report, err)
	case "governed-run":
		return governedRun(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func decodeInput(args []string, target any) error {
	parser := flag.NewFlagSet("json-input", flag.ContinueOnError)
	path := parser.String("file", "", "JSON input file; stdin when omitted")
	encoded := parser.String("json-base64", "", "base64-encoded JSON input; avoids an intermediate file")
	if err := parser.Parse(args); err != nil {
		return err
	}
	if *path != "" && *encoded != "" {
		return errors.New("use only one of --file and --json-base64")
	}
	var reader io.Reader = os.Stdin
	if *path != "" {
		file, err := os.Open(*path)
		if err != nil {
			return err
		}
		defer file.Close()
		reader = file
	} else if *encoded != "" {
		data, err := base64.StdEncoding.DecodeString(*encoded)
		if err != nil {
			return fmt.Errorf("decode --json-base64: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func governedRun(args []string) error {
	parser := flag.NewFlagSet("governed-run", flag.ContinueOnError)
	heartbeat := parser.String("heartbeat", "", "path updated by the governed process")
	stale := parser.Duration("stale-after", 30*time.Second, "heartbeat staleness interval")
	grace := parser.Duration("startup-grace", 30*time.Second, "initial heartbeat grace")
	cwd := parser.String("cwd", "", "child working directory")
	if err := parser.Parse(args); err != nil {
		return err
	}
	command := parser.Args()
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	runtime, err := governance.Open("")
	if err != nil {
		return err
	}
	return runtime.GovernedRun(governance.GovernedRunOptions{Command: command, HeartbeatPath: *heartbeat, StaleAfter: *stale, StartupGrace: *grace, WorkingDir: *cwd})
}

func output(value any, err error) error {
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
