package preflight

import "testing"

func TestCheckHostCapabilitiesPassesWhenRequiredToolsAreAvailable(t *testing.T) {
	t.Parallel()

	result := CheckHostCapabilities(HostCapabilityState{
		AptGetAvailable:         true,
		DpkgAvailable:           true,
		SystemctlAvailable:      true,
		SystemdRuntimeAvailable: true,
	})

	if result.Status != StatusPass {
		t.Fatalf("CheckHostCapabilities() status = %q, want %q", result.Status, StatusPass)
	}
}

func TestCheckHostCapabilitiesFailsWhenSystemdRuntimeIsMissing(t *testing.T) {
	t.Parallel()

	result := CheckHostCapabilities(HostCapabilityState{
		AptGetAvailable:         true,
		DpkgAvailable:           true,
		SystemctlAvailable:      true,
		SystemdRuntimeAvailable: false,
		SystemdRuntimeDetail:    "stat /run/systemd/system: no such file or directory",
	})

	if result.Status != StatusFail {
		t.Fatalf("CheckHostCapabilities() status = %q, want %q", result.Status, StatusFail)
	}
	if len(result.Remediations) == 0 {
		t.Fatal("CheckHostCapabilities() remediations = empty, want systemd guidance")
	}
}
