package realip

import "testing"

func TestSystemdRefreshIntervalNormalizesGoDuration(t *testing.T) {
	t.Parallel()

	got, err := SystemdRefreshInterval("3600000000000ns")
	if err != nil {
		t.Fatalf("SystemdRefreshInterval() error = %v", err)
	}
	if got != "3600s" {
		t.Fatalf("SystemdRefreshInterval() = %q, want 3600s", got)
	}
}

func TestSystemdRefreshIntervalRejectsSubSecondDuration(t *testing.T) {
	t.Parallel()

	_, err := SystemdRefreshInterval("3600000000001ns")
	if err == nil {
		t.Fatal("SystemdRefreshInterval() error = nil, want whole-second failure")
	}
}
