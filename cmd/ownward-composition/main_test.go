package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/capabilitylifecycle"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestStatelessLifecycleCommandsAreDurableAndDoNotEnterProductIdentity(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repository, "manifests", "compositions", "v1", "current-collaborative.json")
	manifest, err := composition.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Identity != "c068ae206a89df9dd2146e98fa875dca80bb05c3ecdafe0343cd825ddd6d751e" {
		t.Fatalf("offline lifecycle changed active product identity: %s", manifest.Identity)
	}
	dependencies := runGo(t, repository, "list", "-deps", "./cmd/ownward")
	if strings.Contains(dependencies, "internal/capabilitylifecycle") {
		t.Fatal("product binary imports offline capability lifecycle")
	}
	lifecycleDependencies := runGo(t, repository, "list", "-deps", "./internal/capabilitylifecycle")
	if strings.Contains(lifecycleDependencies, "internal/authoritysubstrate") {
		t.Fatal("lifecycle package owns or interprets authority persistence instead of using the offline CLI boundary")
	}

	executable := filepath.Join(t.TempDir(), "ownward-composition")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	runGo(t, repository, "build", "-o", executable, "./cmd/ownward-composition")
	workspace := t.TempDir()
	candidateSource := filepath.Join(workspace, "candidate", "access.go")
	writeTestFile(t, candidateSource, "package candidate\n// independently validated access candidate\n")
	candidatePath := filepath.Join(workspace, "candidate.json")
	writeJSON(t, candidatePath, capabilitylifecycle.CandidateArtifact{
		Schema: capabilitylifecycle.CandidateArtifactSchema, Role: "access",
		Content: []capabilitylifecycle.CandidateContent{{Name: "access-candidate", Path: "candidate/access.go"}},
	})
	inspectionOutput := runTool(t, executable, "lifecycle-inspect",
		"--repository", workspace, "--manifest", manifestPath, "--candidate", candidatePath)
	var inspection capabilitylifecycle.CandidateInspection
	if err := json.Unmarshal(inspectionOutput, &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Role != "access" || inspection.StateImpact != capabilitylifecycle.ImpactNone || inspection.Identity == "" {
		t.Fatalf("candidate inspection did not bind the access boundary: %#v", inspection)
	}
	integrationPath := filepath.Join(workspace, "integration.json")
	writeJSON(t, integrationPath, capabilitylifecycle.IntegrationReport{
		Schema: capabilitylifecycle.IntegrationReportSchema, Inspection: inspection.Identity,
		Passed: true, ContractCompatible: true, IntegrationBaselinePassed: true,
		EvidenceSHA256: testDigest("access integration evidence"),
	})
	invalidIntegrationPath := filepath.Join(workspace, "invalid-integration.json")
	writeJSON(t, invalidIntegrationPath, capabilitylifecycle.IntegrationReport{
		Schema: capabilitylifecycle.IntegrationReportSchema, Inspection: testDigest("another inspection"),
		Passed: true, ContractCompatible: true, IntegrationBaselinePassed: true,
		EvidenceSHA256: testDigest("unbound evidence"),
	})
	invalidPlanPath := filepath.Join(workspace, "invalid-plan.json")
	runToolFailure(t, executable, "lifecycle-prepare",
		"--repository", workspace, "--manifest", manifestPath, "--candidate", candidatePath,
		"--integration", invalidIntegrationPath, "--output", invalidPlanPath)
	if _, err := os.Stat(invalidPlanPath); !os.IsNotExist(err) {
		t.Fatal("failed preparation published a plan")
	}
	planPath := filepath.Join(workspace, "plan.json")
	runTool(t, executable, "lifecycle-prepare",
		"--repository", workspace, "--manifest", manifestPath, "--candidate", candidatePath,
		"--integration", integrationPath, "--output", planPath)
	// A new process repeats preparation against the durable plan without
	// rewriting or forking it.
	planBefore, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	runTool(t, executable, "lifecycle-prepare",
		"--repository", workspace, "--manifest", manifestPath, "--candidate", candidatePath,
		"--integration", integrationPath, "--output", planPath)
	planAfter, _ := os.ReadFile(planPath)
	if string(planBefore) != string(planAfter) {
		t.Fatal("repeated prepare rewrote the immutable plan")
	}
	plan, err := capabilitylifecycle.LoadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPlanPath := filepath.Join(workspace, "tampered-plan.json")
	tampered := append([]byte(nil), planBefore...)
	tampered[len(tampered)/2] ^= 1
	if err := os.WriteFile(tamperedPlanPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	runToolFailure(t, executable, "lifecycle-status",
		"--plan", tamperedPlanPath, "--journal", filepath.Join(workspace, "unused-journal"),
		"--data-dir", filepath.Join(workspace, "unused-data"))
	observationPath := filepath.Join(workspace, "accepted-observation.json")
	writeJSON(t, observationPath, capabilitylifecycle.ObservationReport{
		Schema: capabilitylifecycle.ObservationReportSchema, Plan: plan.Identity,
		ActiveComposition: plan.Target.Identity, Passed: true,
		EvidenceSHA256: testDigest("accepted observation"),
	})

	t.Run("missing control is never initialized", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		journalDir := filepath.Join(t.TempDir(), "journal")
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"lifecycle-status", "lifecycle-activate"} {
			runToolFailure(t, executable, command,
				"--plan", planPath, "--journal", journalDir, "--data-dir", dataDir)
		}
		runToolFailure(t, executable, "lifecycle-complete",
			"--plan", planPath, "--journal", journalDir, "--data-dir", dataDir,
			"--observation", observationPath)
		if _, err := os.Stat(filepath.Join(dataDir, "authority", "control.json")); !os.IsNotExist(err) {
			t.Fatalf("lifecycle command created missing authority control: %v", err)
		}
	})

	t.Run("active product owns the offline boundary", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		initializeAuthority(t, dataDir, manifest)
		controlBefore := snapshotTree(t, filepath.Join(dataDir, "authority"))
		assetsBefore := snapshotTree(t, filepath.Join(dataDir, "assets"))
		derivedBefore := snapshotTree(t, filepath.Join(dataDir, "state"))
		authority, err := authoritysubstrate.Open(dataDir, initialControl(manifest))
		if err != nil {
			t.Fatal(err)
		}
		journalDir := filepath.Join(t.TempDir(), "journal")
		for _, command := range []string{"lifecycle-status", "lifecycle-activate"} {
			runToolFailure(t, executable, command,
				"--plan", planPath, "--journal", journalDir, "--data-dir", dataDir)
		}
		runToolFailure(t, executable, "lifecycle-complete",
			"--plan", planPath, "--journal", journalDir, "--data-dir", dataDir,
			"--observation", observationPath)
		if err := authority.Close(); err != nil {
			t.Fatal(err)
		}
		if got := snapshotTree(t, filepath.Join(dataDir, "authority")); !equalSnapshot(got, controlBefore) {
			t.Fatal("occupied lifecycle failure changed authority control")
		}
		if got := snapshotTree(t, filepath.Join(dataDir, "assets")); !equalSnapshot(got, assetsBefore) {
			t.Fatal("occupied lifecycle failure changed assets")
		}
		if got := snapshotTree(t, filepath.Join(dataDir, "state")); !equalSnapshot(got, derivedBefore) {
			t.Fatal("occupied lifecycle failure changed derived state")
		}
	})

	for _, passed := range []bool{true, false} {
		name := "rollback"
		if passed {
			name = "accept"
		}
		t.Run(name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "data")
			initializeAuthority(t, dataDir, manifest)
			assetsBefore := snapshotTree(t, filepath.Join(dataDir, "assets"))
			derivedBefore := snapshotTree(t, filepath.Join(dataDir, "state"))
			journalDir := filepath.Join(t.TempDir(), "journal")

			status := runStatus(t, executable, planPath, journalDir, dataDir, "lifecycle-status")
			if status.Phase != "not-started" || !status.Consistent {
				t.Fatalf("unexpected initial status: %#v", status)
			}
			status = runStatus(t, executable, planPath, journalDir, dataDir, "lifecycle-activate")
			if status.Phase != capabilitylifecycle.PhaseObserving || !status.Consistent || status.ActiveComposition != plan.Target.Identity {
				t.Fatalf("candidate was not activated for observation: %#v", status)
			}
			// Another CLI process recovers the durable state and idempotently
			// performs only the already-completed activation.
			repeated := runStatus(t, executable, planPath, journalDir, dataDir, "lifecycle-activate")
			if repeated.ControlRevision != status.ControlRevision || repeated.JournalRevision != status.JournalRevision {
				t.Fatalf("repeated activation was not a checkpoint recovery: %#v / %#v", status, repeated)
			}
			observationPath := filepath.Join(t.TempDir(), "observation.json")
			writeJSON(t, observationPath, capabilitylifecycle.ObservationReport{
				Schema: capabilitylifecycle.ObservationReportSchema, Plan: plan.Identity,
				ActiveComposition: plan.Target.Identity, Passed: passed,
				EvidenceSHA256: testDigest("observation:" + name),
			})
			completedOutput := runTool(t, executable, "lifecycle-complete",
				"--plan", planPath, "--journal", journalDir, "--data-dir", dataDir, "--observation", observationPath)
			var completed capabilitylifecycle.LifecycleStatus
			if err := json.Unmarshal(completedOutput, &completed); err != nil {
				t.Fatal(err)
			}
			expectedPhase := capabilitylifecycle.PhaseRolledBack
			expectedComposition := manifest.Identity
			if passed {
				expectedPhase = capabilitylifecycle.PhaseAccepted
				expectedComposition = plan.Target.Identity
			}
			if completed.Phase != expectedPhase || completed.ActiveComposition != expectedComposition || !completed.Consistent {
				t.Fatalf("unexpected completed lifecycle: %#v", completed)
			}
			repeatedOutput := runTool(t, executable, "lifecycle-complete",
				"--plan", planPath, "--journal", journalDir, "--data-dir", dataDir, "--observation", observationPath)
			if string(repeatedOutput) != string(completedOutput) {
				t.Fatalf("repeated completion changed durable result:\n%s\n%s", completedOutput, repeatedOutput)
			}
			if got := snapshotTree(t, filepath.Join(dataDir, "assets")); !equalSnapshot(got, assetsBefore) {
				t.Fatal("lifecycle command changed authority assets")
			}
			if got := snapshotTree(t, filepath.Join(dataDir, "state")); !equalSnapshot(got, derivedBefore) {
				t.Fatal("lifecycle command changed derived state")
			}
		})
	}
}

func initializeAuthority(t *testing.T, dataDir string, manifest composition.Manifest) {
	t.Helper()
	authority, err := authoritysubstrate.Open(dataDir, initialControl(manifest))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 2, 3, 4, 0, time.UTC)
	if _, err := authority.Assets().CreateAsset(domain.Information{
		Schema: domain.AssetSchema, ID: "preserved", Revision: 1, CreatedAt: now, UpdatedAt: now,
		Kind: domain.KindKnowledge, Content: "must remain unchanged by the offline lifecycle",
	}); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dataDir, "state", "preserved.marker"), "derived state remains unchanged")
}

func initialControl(manifest composition.Manifest) contract.ControlState {
	kernel := ""
	for _, component := range manifest.Components {
		if component.Role == "kernel" {
			kernel = component.Identity
		}
	}
	return contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: 1,
		ActiveComposition: manifest.Identity, ActiveKernelGeneration: kernel,
	}
}

func runStatus(t *testing.T, executable, plan, journal, dataDir, command string) capabilitylifecycle.LifecycleStatus {
	t.Helper()
	output := runTool(t, executable, command, "--plan", plan, "--journal", journal, "--data-dir", dataDir)
	var status capabilitylifecycle.LifecycleStatus
	if err := json.Unmarshal(output, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func runTool(t *testing.T, executable string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command(executable, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return output
}

func runToolFailure(t *testing.T, executable string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command(executable, arguments...)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded:\n%s", strings.Join(arguments, " "), output)
	}
	return output
}

func runGo(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("go", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, string(append(encoded, '\n')))
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		encoded, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		result[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func equalSnapshot(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	keys := make([]string, 0, len(left))
	for key := range left {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if right[key] != left[key] {
			return false
		}
	}
	return true
}

func testDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
