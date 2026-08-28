//go:build ownward_migration

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritycandidate"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/capabilitylifecycle"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

func TestFormalAuthorityLifecycleRunsInIndependentProcesses(t *testing.T) {
	fixture := newAuthorityCommandFixture(t)
	runAuthorityProcess(t, "prepare", fixture.args()...)
	fixture.updateBaseline(t, 2, "accepted while copying")
	runAuthorityProcess(t, "catch-up", fixture.args()...)
	fixture.updateBaseline(t, 3, "accepted after catch-up")
	runAuthorityProcessFails(t, "promote", fixture.args()...)
	if record := fixture.record(t); record.Phase != capabilitylifecycle.AuthorityPhaseReady || record.Candidate.Versions[0].Revision != 2 {
		t.Fatalf("promote mutated an uncaught candidate: %#v", record)
	}
	runAuthorityProcess(t, "catch-up", fixture.args()...)
	runAuthorityProcess(t, "promote", fixture.args()...)
	runAuthorityProcess(t, "status", fixture.args()...)

	candidate, err := authoritycandidate.Open(fixture.candidate)
	if err != nil {
		t.Fatal(err)
	}
	control, err := openCandidateControl(fixture.data, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	guard := &capabilitylifecycle.ActiveAuthority{Assets: candidate, Control: control, Composition: fixture.plan.Target.Identity}
	current, _ := candidate.ReadCurrent("asset")
	current.Revision++
	current.UpdatedAt = current.UpdatedAt.Add(time.Minute)
	current.Content = "accepted during observation"
	if _, err := guard.UpdateAsset(current, 3); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	observation := capabilitylifecycle.AuthorityObservation{Schema: capabilitylifecycle.AuthorityObservationSchema, Plan: fixture.plan.Identity, ActiveComposition: fixture.plan.Target.Identity, ReportSHA256: authorityCommandDigest("pass"), Passed: true}
	writeAuthorityJSON(t, fixture.observation, observation)
	runAuthorityProcess(t, "observe", fixture.args("--observation", fixture.observation, "--backup", fixture.backup)...)
	runAuthorityProcess(t, "observe", fixture.args("--observation", fixture.observation, "--backup", fixture.backup)...)
	record := fixture.record(t)
	if record.Phase != capabilitylifecycle.AuthorityPhaseAccepted || record.Candidate.AssetCount != 1 || record.RecoveryBackup == "" {
		t.Fatalf("formal authority lifecycle did not accept the complete candidate: %#v", record)
	}
	restoredControl, err := authoritycandidate.RestoreAuthority(fixture.backup, filepath.Join(fixture.root, "restored"))
	if err != nil || restoredControl.ActiveComposition != fixture.plan.Target.Identity {
		t.Fatalf("formal accepted backup is not recoverable: %#v %v", restoredControl, err)
	}
	if _, err := authoritysubstrate.Open(fixture.data, authorityInitial(fixture.plan.Baseline)); err == nil {
		t.Fatal("baseline product could reopen after candidate became authoritative")
	}
}

func TestFormalAuthorityLifecycleRollbackCatchesObservationUpdates(t *testing.T) {
	fixture := newAuthorityCommandFixture(t)
	runAuthorityProcess(t, "prepare", fixture.args()...)
	runAuthorityProcess(t, "promote", fixture.args()...)
	candidate, _ := authoritycandidate.Open(fixture.candidate)
	control, _ := openCandidateControl(fixture.data, fixture.plan)
	guard := &capabilitylifecycle.ActiveAuthority{Assets: candidate, Control: control, Composition: fixture.plan.Target.Identity}
	current, _ := candidate.ReadCurrent("asset")
	current.Revision++
	current.UpdatedAt = current.UpdatedAt.Add(time.Minute)
	current.Content = "must survive rollback"
	if _, err := guard.UpdateAsset(current, 1); err != nil {
		t.Fatal(err)
	}
	current.Revision++
	current.UpdatedAt = current.UpdatedAt.Add(time.Minute)
	current.Content = "second accepted update must also survive rollback"
	if _, err := guard.UpdateAsset(current, 2); err != nil {
		t.Fatal(err)
	}
	_ = candidate.Close()
	observation := capabilitylifecycle.AuthorityObservation{Schema: capabilitylifecycle.AuthorityObservationSchema, Plan: fixture.plan.Identity, ActiveComposition: fixture.plan.Target.Identity, ReportSHA256: authorityCommandDigest("fail"), Passed: false}
	writeAuthorityJSON(t, fixture.observation, observation)
	runAuthorityProcess(t, "observe", fixture.args("--observation", fixture.observation)...)
	runAuthorityProcess(t, "status", fixture.args()...)
	source, err := authoritysubstrate.Open(fixture.data, authorityInitial(fixture.plan.Baseline))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	actual, ok := source.Assets().ReadCurrent("asset")
	if !ok || actual.Revision != 3 || actual.Content != current.Content {
		t.Fatalf("formal rollback lost an accepted update: %#v", actual)
	}
}

func TestFormalAuthorityPlanBindsCandidateContentAndRejectsDrift(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "candidate-root")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	contentPath := filepath.Join(repository, "candidate-store.go")
	if err := os.WriteFile(contentPath, []byte("package candidate\nconst format = \"authority-v1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	baselinePath := filepath.Join(root, "baseline.json")
	candidatePath := filepath.Join(root, "candidate.json")
	integrationPath := filepath.Join(root, "integration.json")
	planPath := filepath.Join(root, "plan.json")
	writeAuthorityJSON(t, baselinePath, baseline)
	writeAuthorityJSON(t, candidatePath, capabilitylifecycle.CandidateArtifact{Schema: capabilitylifecycle.CandidateArtifactSchema, Role: "authority-substrate", Content: []capabilitylifecycle.CandidateContent{{Name: "candidate-store", Path: "candidate-store.go"}}})
	inspection, err := capabilitylifecycle.InspectCandidate(repository, baselinePath, candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	writeAuthorityJSON(t, integrationPath, capabilitylifecycle.AuthorityValidation{
		Schema: capabilitylifecycle.AuthorityValidationSchema, CandidateComponent: inspection.CandidateComponent,
		BaselineComposition: inspection.BaselineComposition, TargetComposition: inspection.TargetComposition,
		StateImpact: capabilitylifecycle.ImpactAuthority, CandidateFormat: authoritycandidate.Format,
		IntegrationSHA256: authorityCommandDigest("artifact-integration"), AssetSemanticsPassed: true,
		BackupRestorePassed: true, ExclusiveWriterPassed: true, IntegrationBaselinePass: true,
	})
	if err := run([]string{"plan", "--repository", repository, "--baseline", baselinePath, "--candidate", candidatePath, "--integration", integrationPath, "--output", planPath}); err != nil {
		t.Fatal(err)
	}
	plan, err := capabilitylifecycle.LoadAuthorityPlan(planPath)
	if err != nil || plan.Replacement.Identity != inspection.CandidateComponent || plan.Target.Identity != inspection.TargetComposition {
		t.Fatalf("formal authority plan identity mismatch: %#v %v", plan, err)
	}
	if err := os.WriteFile(contentPath, []byte("package candidate\nconst format = \"tampered\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"plan", "--repository", repository, "--baseline", baselinePath, "--candidate", candidatePath, "--integration", integrationPath, "--output", filepath.Join(root, "tampered-plan.json")}); err == nil {
		t.Fatal("candidate content drift reused the old integration identity")
	}
}

func TestAuthorityLifecycleRejectsMigrationArtifactsInsideStateDirectories(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	candidate := filepath.Join(root, "candidate")
	journal := filepath.Join(root, "journal")
	for name, artifact := range map[string]string{
		"plan-in-data":      filepath.Join(data, "plan.json"),
		"plan-in-candidate": filepath.Join(candidate, "plan.json"),
		"plan-in-journal":   filepath.Join(journal, "plan.json"),
	} {
		t.Run(name, func(t *testing.T) {
			err := run([]string{"status", "--plan", artifact, "--journal", journal, "--data-dir", data, "--candidate-dir", candidate})
			if err == nil || !strings.Contains(err.Error(), "迁移制品") {
				t.Fatalf("state-contained plan was not rejected before opening state: %v", err)
			}
		})
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("invalid lifecycle paths created the journal directory")
	}
}

func TestAuthorityLifecycleRejectsBackupInsideAuthorityState(t *testing.T) {
	fixture := newAuthorityCommandFixture(t)
	observation := capabilitylifecycle.AuthorityObservation{Schema: capabilitylifecycle.AuthorityObservationSchema, Plan: fixture.plan.Identity, ActiveComposition: fixture.plan.Target.Identity, ReportSHA256: authorityCommandDigest("pass"), Passed: true}
	writeAuthorityJSON(t, fixture.observation, observation)
	for name, backup := range map[string]string{
		"data":      filepath.Join(fixture.data, "backup.ownward"),
		"candidate": filepath.Join(fixture.candidate, "backup.ownward"),
		"journal":   filepath.Join(fixture.journal, "backup.ownward"),
	} {
		t.Run(name, func(t *testing.T) {
			err := run([]string{"observe", "--plan", fixture.planPath, "--journal", fixture.journal, "--data-dir", fixture.data, "--candidate-dir", fixture.candidate, "--observation", fixture.observation, "--backup", backup})
			if err == nil || !strings.Contains(err.Error(), "迁移制品") {
				t.Fatalf("state-contained backup was not rejected: %v", err)
			}
		})
	}
}

func TestAuthorityLifecycleCommandProcess(t *testing.T) {
	if os.Getenv("OWNWARD_AUTHORITY_COMMAND_HELPER") != "1" {
		return
	}
	separator := -1
	for index, value := range os.Args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		t.Fatal("missing authority helper arguments")
	}
	if err := run(os.Args[separator+1:]); err != nil {
		t.Fatal(err)
	}
}

type authorityCommandFixture struct {
	root, data, candidate, journal, planPath, observation, backup string
	plan                                                          capabilitylifecycle.AuthorityPlan
}

func newAuthorityCommandFixture(t *testing.T) authorityCommandFixture {
	t.Helper()
	current, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	var replacement composition.Component
	for _, component := range current.Components {
		if component.Role == "authority-substrate" {
			replacement = component
			break
		}
	}
	replacement.Content = append([]composition.Content(nil), replacement.Content...)
	replacement.Content[0].SHA256 = authorityCommandDigest("formal-candidate-content")
	replacement.Identity, _ = composition.ComponentIdentity(replacement)
	// Use the lifecycle implementation to propagate all dependent identities.
	validationTarget := capabilitylifecycle.AuthorityValidation{Schema: capabilitylifecycle.AuthorityValidationSchema, CandidateComponent: replacement.Identity, BaselineComposition: current.Identity, StateImpact: capabilitylifecycle.ImpactAuthority, CandidateFormat: authoritycandidate.Format, IntegrationSHA256: authorityCommandDigest("formal-integration"), AssetSemanticsPassed: true, BackupRestorePassed: true, ExclusiveWriterPassed: true, IntegrationBaselinePass: true}
	propagated, err := capabilitylifecycle.InspectAuthorityTarget(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	validationTarget.TargetComposition = propagated.Identity
	plan, err := capabilitylifecycle.PrepareAuthority(current, replacement, validationTarget)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	fixture := authorityCommandFixture{root: root, data: filepath.Join(root, "data"), candidate: filepath.Join(root, "candidate"), journal: filepath.Join(root, "journal"), planPath: filepath.Join(root, "plan.json"), observation: filepath.Join(root, "observation.json"), backup: filepath.Join(root, "candidate.ownward"), plan: plan}
	if err := capabilitylifecycle.WriteAuthorityPlan(fixture.planPath, plan); err != nil {
		t.Fatal(err)
	}
	source, err := authoritysubstrate.Open(fixture.data, authorityInitial(current))
	if err != nil {
		t.Fatal(err)
	}
	asset := domain.Information{Schema: domain.AssetSchema, ID: "asset", Revision: 1, CreatedAt: time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC), Kind: domain.KindKnowledge, Content: "formal baseline"}
	if _, err := source.Assets().CreateAsset(asset); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture authorityCommandFixture) args(extra ...string) []string {
	args := []string{"--plan", fixture.planPath, "--journal", fixture.journal, "--data-dir", fixture.data, "--candidate-dir", fixture.candidate}
	return append(args, extra...)
}

func (fixture authorityCommandFixture) updateBaseline(t *testing.T, revision uint64, content string) {
	t.Helper()
	source, err := authoritysubstrate.Open(fixture.data, authorityInitial(fixture.plan.Baseline))
	if err != nil {
		t.Fatal(err)
	}
	value, _ := source.Assets().ReadCurrent("asset")
	value.Revision = revision
	value.UpdatedAt = value.UpdatedAt.Add(time.Minute)
	value.Content = content
	if _, err := source.Assets().UpdateAsset(value, revision-1); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func (fixture authorityCommandFixture) record(t *testing.T) capabilitylifecycle.AuthorityRecord {
	t.Helper()
	journal, _ := capabilitylifecycle.OpenAuthorityJournal(fixture.journal)
	record, exists, err := journal.Read()
	if err != nil || !exists {
		t.Fatalf("missing formal authority record: %v", err)
	}
	return record
}

func runAuthorityProcess(t *testing.T, command string, args ...string) {
	t.Helper()
	commandArgs := []string{"-test.run=TestAuthorityLifecycleCommandProcess", "--", command}
	commandArgs = append(commandArgs, args...)
	process := exec.Command(os.Args[0], commandArgs...)
	process.Env = append(os.Environ(), "OWNWARD_AUTHORITY_COMMAND_HELPER=1")
	output, err := process.CombinedOutput()
	if err != nil {
		t.Fatalf("authority command %s failed: %v\n%s", command, err, output)
	}
}

func runAuthorityProcessFails(t *testing.T, command string, args ...string) {
	t.Helper()
	commandArgs := []string{"-test.run=TestAuthorityLifecycleCommandProcess", "--", command}
	commandArgs = append(commandArgs, args...)
	process := exec.Command(os.Args[0], commandArgs...)
	process.Env = append(os.Environ(), "OWNWARD_AUTHORITY_COMMAND_HELPER=1")
	if output, err := process.CombinedOutput(); err == nil {
		t.Fatalf("authority command %s unexpectedly succeeded\n%s", command, output)
	} else if !strings.Contains(string(output), "尚未在最终屏障外追平") {
		t.Fatalf("authority command %s failed for the wrong reason: %v\n%s", command, err, output)
	}
}

func authorityInitial(manifest composition.Manifest) contract.ControlState {
	for _, component := range manifest.Components {
		if component.Role == "kernel" {
			return contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: manifest.Identity, ActiveKernelGeneration: component.Identity}
		}
	}
	return contract.ControlState{}
}

func writeAuthorityJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil || os.WriteFile(path, append(encoded, '\n'), 0o600) != nil {
		t.Fatal("write authority fixture")
	}
}

func authorityCommandDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
