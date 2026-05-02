package preflight

import "testing"

func TestCheckPackageSourceModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  PackageSourceState
		status Status
	}{
		{
			name: "direct missing reachability confirmation",
			input: PackageSourceState{
				Mode:    "direct",
				Version: "0.28.0",
			},
			status: StatusFail,
		},
		{
			name: "direct missing integrity evidence",
			input: PackageSourceState{
				Mode:                "direct",
				Version:             "0.28.0",
				ReachabilityChecked: true,
				Reachable:           true,
			},
			status: StatusFail,
		},
		{
			name: "direct verified",
			input: PackageSourceState{
				Mode:                "direct",
				Version:             "0.28.0",
				ExpectedSHA256:      "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
				ReachabilityChecked: true,
				Reachable:           true,
				IntegrityChecked:    true,
				ActualSHA256:        "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
			status: StatusPass,
		},
		{
			name: "mirror checksum mismatch",
			input: PackageSourceState{
				Mode:                "mirror",
				Version:             "0.28.0",
				URL:                 "https://mirror.example.com/headscale.deb",
				ExpectedSHA256:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ReachabilityChecked: true,
				Reachable:           true,
				IntegrityChecked:    true,
				ActualSHA256:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
			status: StatusFail,
		},
		{
			name: "mirror reachability not confirmed",
			input: PackageSourceState{
				Mode:           "mirror",
				Version:        "0.28.0",
				URL:            "https://mirror.example.com/headscale.deb",
				ExpectedSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			status: StatusFail,
		},
		{
			name: "mirror integrity not confirmed",
			input: PackageSourceState{
				Mode:                "mirror",
				Version:             "0.28.0",
				URL:                 "https://mirror.example.com/headscale.deb",
				ExpectedSHA256:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ReachabilityChecked: true,
				Reachable:           true,
			},
			status: StatusFail,
		},
		{
			name: "offline package missing",
			input: PackageSourceState{
				Mode:             "offline",
				Version:          "0.28.0",
				FilePath:         "/tmp/headscale.deb",
				ExpectedSHA256:   "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				FileExists:       false,
				IntegrityChecked: true,
			},
			status: StatusFail,
		},
		{
			name: "offline integrity not confirmed",
			input: PackageSourceState{
				Mode:           "offline",
				Version:        "0.28.0",
				FilePath:       "/tmp/headscale.deb",
				ExpectedSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				FileExists:     true,
			},
			status: StatusFail,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := CheckPackageSource(tt.input)
			if result.Status != tt.status {
				t.Fatalf("CheckPackageSource() status = %q, want %q", result.Status, tt.status)
			}
			if result.Summary == "" {
				t.Fatal("CheckPackageSource() summary = empty, want actionable summary")
			}
		})
	}
}
