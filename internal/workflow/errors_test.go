package workflow

import (
	"errors"
	"testing"
)

func TestFailureResponseFormatsUserReadableSummary(t *testing.T) {
	t.Parallel()

	failure := Failure{
		Step:      "install packages",
		Operation: "headscale package installation did not complete",
		Impact:    "deploy cannot continue until host packages are installed",
		Remediation: []string{
			"Check package mirror reachability or switch to a verified offline package.",
			"Confirm the configured package checksum matches the artifact you expect to install.",
		},
		RetryCommand: "meshify deploy --config meshify.yaml",
		Cause:        errors.New("apt-get install headscale exited with status 100\nraw shell spew that should stay hidden"),
	}

	response := failure.Response("deploy")
	if response.Command != "deploy" {
		t.Fatalf("response.Command = %q, want %q", response.Command, "deploy")
	}
	if response.Status != "failed" {
		t.Fatalf("response.Status = %q, want %q", response.Status, "failed")
	}
	if response.Summary != "install packages failed: headscale package installation did not complete" {
		t.Fatalf("response.Summary = %q, want failure summary", response.Summary)
	}
	if len(response.Fields) != 4 {
		t.Fatalf("len(response.Fields) = %d, want 4", len(response.Fields))
	}
	if response.Fields[2].Label != "impact" || response.Fields[2].Value != "deploy cannot continue until host packages are installed" {
		t.Fatalf("impact field = %#v, want user-readable impact", response.Fields[2])
	}
	if response.Fields[3].Value != "apt-get install headscale exited with status 100" {
		t.Fatalf("details = %q, want sanitized single-line cause", response.Fields[3].Value)
	}
	if len(response.NextSteps) != 3 {
		t.Fatalf("len(response.NextSteps) = %d, want 3", len(response.NextSteps))
	}
	if response.NextSteps[2] != "Retry after remediation: meshify deploy --config meshify.yaml" {
		t.Fatalf("retry step = %q, want retry command", response.NextSteps[2])
	}
	if failure.Error() != response.Summary {
		t.Fatalf("Error() = %q, want %q", failure.Error(), response.Summary)
	}
}

func TestFailureSnapshotCarriesSerializableUserContext(t *testing.T) {
	t.Parallel()

	failure := Failure{
		Step:      "install runtime assets",
		Operation: "writing /etc/nginx/sites-available/headscale.conf",
		Impact:    "the reverse proxy configuration is incomplete",
		Remediation: []string{
			"Check the destination directory permissions.",
		},
		RetryCommand: "meshify deploy --config meshify.yaml",
		Cause:        errors.New("write failed\nraw shell spew"),
	}

	snapshot := failure.Snapshot()
	if snapshot.Summary != "install runtime assets failed: writing /etc/nginx/sites-available/headscale.conf" {
		t.Fatalf("Summary = %q, want failure summary", snapshot.Summary)
	}
	if snapshot.Details != "write failed" {
		t.Fatalf("Details = %q, want sanitized single-line cause", snapshot.Details)
	}
	if len(snapshot.Remediation) != 1 || snapshot.Remediation[0] != "Check the destination directory permissions." {
		t.Fatalf("Remediation = %v, want serialized remediation", snapshot.Remediation)
	}
	if snapshot.RetryCommand != "meshify deploy --config meshify.yaml" {
		t.Fatalf("RetryCommand = %q, want serialized retry command", snapshot.RetryCommand)
	}
}
