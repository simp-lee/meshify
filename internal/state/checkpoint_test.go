package state

import (
	"meshify/internal/assets"
	"meshify/internal/workflow"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointMutationDeduplicatesState(t *testing.T) {
	t.Parallel()

	var checkpoint Checkpoint
	if !checkpoint.MarkCompleted("packages-installed") {
		t.Fatal("MarkCompleted() = false, want true")
	}
	if checkpoint.MarkCompleted("packages-installed") {
		t.Fatal("second MarkCompleted() = true, want false")
	}
	if !checkpoint.RecordModifiedPaths("/etc/headscale/config.yaml", "/etc/headscale/config.yaml", "/etc/nginx/sites-available/headscale.conf") {
		t.Fatal("RecordModifiedPaths() = false, want true")
	}
	if !checkpoint.RecordActivations(assets.ActivationRestartHeadscale, assets.ActivationRestartHeadscale, assets.ActivationReloadNginx) {
		t.Fatal("RecordActivations() = false, want true")
	}

	if checkpoint.CurrentCheckpoint != "packages-installed" {
		t.Fatalf("CurrentCheckpoint = %q, want %q", checkpoint.CurrentCheckpoint, "packages-installed")
	}
	if len(checkpoint.CompletedCheckpoints) != 1 {
		t.Fatalf("len(CompletedCheckpoints) = %d, want 1", len(checkpoint.CompletedCheckpoints))
	}
	if len(checkpoint.ModifiedPaths) != 2 {
		t.Fatalf("len(ModifiedPaths) = %d, want 2", len(checkpoint.ModifiedPaths))
	}
	if len(checkpoint.ActivationHistory) != 2 {
		t.Fatalf("len(ActivationHistory) = %d, want 2", len(checkpoint.ActivationHistory))
	}
	if checkpoint.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want mutation timestamp")
	}
	if !checkpoint.HasCompleted("packages-installed") {
		t.Fatal("HasCompleted() = false, want true")
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "checkpoint.json")
	store := NewStore(path)

	checkpoint := Checkpoint{DesiredStateDigest: "desired-state-a"}
	checkpoint.MarkCompleted("staged-files-written")
	checkpoint.RecordModifiedPaths("/etc/headscale/config.yaml")
	checkpoint.RecordActivations(assets.ActivationRestartHeadscale)
	checkpoint.RecordFailure(workflow.Failure{
		Step:         "install runtime assets",
		Operation:    "writing /etc/headscale/config.yaml",
		Impact:       "deploy cannot continue until runtime config is installed",
		Remediation:  []string{"Check filesystem permissions and rerun deploy."},
		RetryCommand: "meshify deploy --config meshify.yaml",
	}.Snapshot())

	if err := store.Save(checkpoint); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.CurrentCheckpoint != "staged-files-written" {
		t.Fatalf("CurrentCheckpoint = %q, want %q", loaded.CurrentCheckpoint, "staged-files-written")
	}
	if loaded.DesiredStateDigest != "desired-state-a" {
		t.Fatalf("DesiredStateDigest = %q, want %q", loaded.DesiredStateDigest, "desired-state-a")
	}
	if len(loaded.CompletedCheckpoints) != 1 || loaded.CompletedCheckpoints[0] != "staged-files-written" {
		t.Fatalf("CompletedCheckpoints = %v, want [staged-files-written]", loaded.CompletedCheckpoints)
	}
	if len(loaded.ModifiedPaths) != 1 || loaded.ModifiedPaths[0] != "/etc/headscale/config.yaml" {
		t.Fatalf("ModifiedPaths = %v, want [/etc/headscale/config.yaml]", loaded.ModifiedPaths)
	}
	if len(loaded.ActivationHistory) != 1 || loaded.ActivationHistory[0] != assets.ActivationRestartHeadscale {
		t.Fatalf("ActivationHistory = %v, want [%q]", loaded.ActivationHistory, assets.ActivationRestartHeadscale)
	}
	if loaded.LastFailure == nil {
		t.Fatal("LastFailure = nil, want persisted failure snapshot")
	}
	if loaded.LastFailure.Summary != "install runtime assets failed: writing /etc/headscale/config.yaml" {
		t.Fatalf("LastFailure.Summary = %q, want persisted failure summary", loaded.LastFailure.Summary)
	}
	if len(loaded.LastFailure.Remediation) != 1 || loaded.LastFailure.Remediation[0] != "Check filesystem permissions and rerun deploy." {
		t.Fatalf("LastFailure.Remediation = %v, want persisted remediation", loaded.LastFailure.Remediation)
	}
	if loaded.LastFailure.RetryCommand != "meshify deploy --config meshify.yaml" {
		t.Fatalf("LastFailure.RetryCommand = %q, want persisted retry command", loaded.LastFailure.RetryCommand)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want persisted timestamp")
	}
}

func TestStoreSaveDoesNotCollideWithStaleFixedTempFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "checkpoint.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path+".tmp", []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile(stale temp) error = %v", err)
	}

	store := NewStore(path)
	checkpoint := Checkpoint{DesiredStateDigest: "desired-state-a"}
	checkpoint.MarkCompleted("packages-installed")

	if err := store.Save(checkpoint); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.HasCompleted("packages-installed") {
		t.Fatalf("loaded checkpoint = %#v, want completed packages-installed", loaded)
	}
}

func TestCheckpointBeginDeployResetsRetiredHistoryAndNewDesiredState(t *testing.T) {
	t.Parallel()

	checkpoint := Checkpoint{DesiredStateDigest: "desired-state-a"}
	checkpoint.MarkCompleted("runtime-assets-installed")
	checkpoint.RecordModifiedPaths("/etc/headscale/config.yaml")
	checkpoint.RecordActivations(assets.ActivationRestartHeadscale)
	checkpoint.RecordFailure(workflow.Failure{Step: "install runtime assets", Operation: "writing /etc/headscale/config.yaml"}.Snapshot())

	if !checkpoint.FinalizeSuccessfulDeploy() {
		t.Fatal("FinalizeSuccessfulDeploy() = false, want true")
	}
	if checkpoint.CurrentCheckpoint != "" {
		t.Fatalf("CurrentCheckpoint = %q, want empty", checkpoint.CurrentCheckpoint)
	}
	if checkpoint.LastFailure != nil {
		t.Fatalf("LastFailure = %#v, want nil", checkpoint.LastFailure)
	}
	if len(checkpoint.CompletedCheckpoints) != 1 {
		t.Fatalf("len(CompletedCheckpoints) = %d, want 1 retained history entry", len(checkpoint.CompletedCheckpoints))
	}

	if !checkpoint.BeginDeploy("desired-state-a") {
		t.Fatal("BeginDeploy(same digest) = false, want true")
	}
	if len(checkpoint.CompletedCheckpoints) != 0 {
		t.Fatalf("CompletedCheckpoints = %v, want cleared retired history", checkpoint.CompletedCheckpoints)
	}
	if len(checkpoint.ModifiedPaths) != 0 {
		t.Fatalf("ModifiedPaths = %v, want cleared retired modifications", checkpoint.ModifiedPaths)
	}
	if len(checkpoint.ActivationHistory) != 0 {
		t.Fatalf("ActivationHistory = %v, want cleared retired activations", checkpoint.ActivationHistory)
	}

	checkpoint.MarkCompleted("runtime-assets-installed")
	checkpoint.RecordModifiedPaths("/etc/headscale/config.yaml")
	checkpoint.RecordActivations(assets.ActivationRestartHeadscale)
	checkpoint.RecordFailure(workflow.Failure{Step: "install runtime assets", Operation: "writing /etc/headscale/config.yaml"}.Snapshot())

	if !checkpoint.BeginDeploy("desired-state-b") {
		t.Fatal("BeginDeploy(new digest) = false, want true")
	}
	if checkpoint.DesiredStateDigest != "desired-state-b" {
		t.Fatalf("DesiredStateDigest = %q, want %q", checkpoint.DesiredStateDigest, "desired-state-b")
	}
	if checkpoint.CurrentCheckpoint != "" {
		t.Fatalf("CurrentCheckpoint = %q, want empty after desired state change", checkpoint.CurrentCheckpoint)
	}
	if len(checkpoint.CompletedCheckpoints) != 0 {
		t.Fatalf("CompletedCheckpoints = %v, want cleared on desired state change", checkpoint.CompletedCheckpoints)
	}
	if len(checkpoint.ModifiedPaths) != 0 {
		t.Fatalf("ModifiedPaths = %v, want cleared on desired state change", checkpoint.ModifiedPaths)
	}
	if len(checkpoint.ActivationHistory) != 0 {
		t.Fatalf("ActivationHistory = %v, want cleared on desired state change", checkpoint.ActivationHistory)
	}
	if checkpoint.LastFailure != nil {
		t.Fatalf("LastFailure = %#v, want nil on desired state change", checkpoint.LastFailure)
	}
}
