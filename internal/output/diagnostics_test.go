package output

import (
	"bytes"
	"meshify/internal/preflight"
	"strings"
	"testing"
)

func TestDiagnosticsFormatterWritesHumanSummary(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	formatter := NewDiagnosticsFormatter(&buffer, FormatHuman)
	report := preflight.Report{
		Checks: []preflight.CheckResult{
			{
				ID:       "platform",
				Title:    "Supported platform",
				Status:   preflight.StatusFail,
				Severity: preflight.SeverityError,
				Summary:  "Fedora Linux 41 is outside the server support matrix.",
				Remediations: []string{
					"Use Debian, Ubuntu, or a Debian-family distribution that reports debian or ubuntu in ID_LIKE.",
				},
			},
			{
				ID:       "cloud-manual",
				Title:    "Cloud and compliance checklist",
				Status:   preflight.StatusManual,
				Severity: preflight.SeverityManual,
				Summary:  "Manual checks are still required before deploy.",
			},
		},
		ManualChecklists: []preflight.ManualChecklist{
			{
				Title: "China mainland ingress review",
				Items: []string{
					"Confirm cloud security group allows 80/tcp, 443/tcp, and 3478/udp.",
					"Confirm ICP filing or cloud access prerequisites are satisfied before public launch.",
				},
			},
		},
	}

	if err := formatter.WritePreflight(report); err != nil {
		t.Fatalf("WritePreflight() error = %v", err)
	}

	output := buffer.String()
	for _, want := range []string{
		"meshify preflight: blocked by 1 failed check",
		"[FAIL] Supported platform",
		"Remediation:",
		"Manual checklist:",
		"3478/udp",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("WritePreflight() output = %q, want substring %q", output, want)
		}
	}
}

func TestDiagnosticsFormatterWritesJSONEnvelope(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	formatter := NewDiagnosticsFormatter(&buffer, FormatJSON)
	report := preflight.Report{
		Checks: []preflight.CheckResult{
			{
				ID:       "permissions",
				Title:    "Root or sudo",
				Status:   preflight.StatusPass,
				Severity: preflight.SeverityInfo,
				Summary:  "Current user can elevate with sudo.",
			},
		},
	}

	if err := formatter.WritePreflight(report); err != nil {
		t.Fatalf("WritePreflight() error = %v", err)
	}

	encoded := buffer.String()
	for _, want := range []string{"\"command\":\"preflight\"", "\"status\":\"pass\"", "\"checks\""} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("WritePreflight() JSON = %q, want substring %q", encoded, want)
		}
	}
}

func TestDiagnosticsFormatterUsesCommandHeading(t *testing.T) {
	t.Parallel()

	commands := []string{"deploy", "verify"}
	for _, command := range commands {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			formatter := NewDiagnosticsFormatter(&buffer, FormatHuman)
			if err := formatter.WriteReport(command, preflight.Report{}); err != nil {
				t.Fatalf("WriteReport() error = %v", err)
			}

			want := "meshify " + command + ": all checks passed"
			if !strings.Contains(buffer.String(), want) {
				t.Fatalf("WriteReport() output = %q, want substring %q", buffer.String(), want)
			}
		})
	}
}
