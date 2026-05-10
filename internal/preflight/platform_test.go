package preflight

import "testing"

func TestParseOSReleaseSupportsOfficialQuotingAndEscapes(t *testing.T) {
	t.Parallel()

	info := ParseOSRelease(`
ID='debian'
ID_LIKE="debian"
VERSION_ID="13"
PRETTY_NAME="Debian GNU/Linux 13 \"trixie\""
`)

	if info.ID != "debian" {
		t.Fatalf("ID = %q, want debian", info.ID)
	}
	if info.IDLike != "debian" {
		t.Fatalf("IDLike = %q, want debian", info.IDLike)
	}
	if info.VersionID != "13" {
		t.Fatalf("VersionID = %q, want 13", info.VersionID)
	}
	if info.PrettyName != `Debian GNU/Linux 13 "trixie"` {
		t.Fatalf("PrettyName = %q", info.PrettyName)
	}
}

func TestCheckPlatformSupportsDebianFamily(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		platform PlatformInfo
	}{
		{
			name: "debian 13",
			platform: PlatformInfo{
				ID:         "debian",
				VersionID:  "13",
				PrettyName: "Debian GNU/Linux 13 (trixie)",
			},
		},
		{
			name: "ubuntu 22 lts",
			platform: PlatformInfo{
				ID:         "ubuntu",
				VersionID:  "22.04",
				PrettyName: "Ubuntu 22.04.5 LTS",
			},
		},
		{
			name: "debian family through id_like",
			platform: PlatformInfo{
				ID:         "linuxmint",
				IDLike:     "ubuntu debian",
				VersionID:  "22",
				PrettyName: "Linux Mint 22",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := CheckPlatform(tt.platform)
			if result.Status != StatusPass {
				t.Fatalf("CheckPlatform() status = %q, want %q", result.Status, StatusPass)
			}
			if result.Severity != SeverityInfo {
				t.Fatalf("CheckPlatform() severity = %q, want %q", result.Severity, SeverityInfo)
			}
		})
	}
}

func TestCheckPlatformRejectsNonDebianFamilyDistribution(t *testing.T) {
	t.Parallel()

	result := CheckPlatform(PlatformInfo{
		ID:         "fedora",
		IDLike:     "rhel fedora",
		VersionID:  "41",
		PrettyName: "Fedora Linux 41",
	})

	if result.Status != StatusFail {
		t.Fatalf("CheckPlatform() status = %q, want %q", result.Status, StatusFail)
	}
	if result.Severity != SeverityError {
		t.Fatalf("CheckPlatform() severity = %q, want %q", result.Severity, SeverityError)
	}
	if result.Summary == "" {
		t.Fatal("CheckPlatform() summary = empty, want support matrix guidance")
	}
	if len(result.Remediations) == 0 {
		t.Fatal("CheckPlatform() remediations = empty, want supported matrix guidance")
	}
}
