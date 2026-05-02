package output

import (
	"encoding/json"
	"fmt"
	"io"
	"meshify/internal/preflight"
	"strings"
)

type DiagnosticsFormatter struct {
	writer io.Writer
	format Format
}

type diagnosticsEnvelope struct {
	Command          string                      `json:"command"`
	Status           preflight.Status            `json:"status"`
	Summary          string                      `json:"summary"`
	Checks           []preflight.CheckResult     `json:"checks"`
	ManualChecklists []preflight.ManualChecklist `json:"manual_checklists,omitempty"`
	NextSteps        []string                    `json:"next_steps,omitempty"`
}

func NewDiagnosticsFormatter(writer io.Writer, format Format) DiagnosticsFormatter {
	return DiagnosticsFormatter{writer: writer, format: format}
}

func (formatter DiagnosticsFormatter) WritePreflight(report preflight.Report) error {
	return formatter.WriteReport("preflight", report)
}

func (formatter DiagnosticsFormatter) WriteReport(command string, report preflight.Report) error {
	envelope := diagnosticsEnvelope{
		Command:          command,
		Status:           report.OverallStatus(),
		Summary:          report.Summary(),
		Checks:           report.Checks,
		ManualChecklists: report.ManualChecklists,
		NextSteps:        report.NextSteps(),
	}

	switch formatter.format {
	case FormatHuman:
		return writePreflightHuman(formatter.writer, envelope)
	case FormatJSON:
		encoder := json.NewEncoder(formatter.writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(envelope)
	default:
		return fmt.Errorf("unsupported output format %q", formatter.format)
	}
}

func writePreflightHuman(writer io.Writer, envelope diagnosticsEnvelope) error {
	command := strings.TrimSpace(envelope.Command)
	if command == "" {
		command = "preflight"
	}
	if _, err := fmt.Fprintf(writer, "meshify %s: %s\n", command, envelope.Summary); err != nil {
		return err
	}

	if len(envelope.Checks) > 0 {
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
		for _, check := range envelope.Checks {
			if _, err := fmt.Fprintf(writer, "[%s] %s\n", statusLabel(check.Status), check.Title); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(writer, "  %s\n", check.Summary); err != nil {
				return err
			}
			for _, finding := range check.Findings {
				if _, err := fmt.Fprintf(writer, "  - %s\n", finding); err != nil {
					return err
				}
			}
			if len(check.Remediations) > 0 {
				if _, err := fmt.Fprintln(writer, "  Remediation:"); err != nil {
					return err
				}
				for index, remediation := range check.Remediations {
					if _, err := fmt.Fprintf(writer, "    %d. %s\n", index+1, remediation); err != nil {
						return err
					}
				}
			}
		}
	}

	if len(envelope.ManualChecklists) > 0 {
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(writer, "Manual checklist:"); err != nil {
			return err
		}
		for _, checklist := range envelope.ManualChecklists {
			if _, err := fmt.Fprintf(writer, "- %s\n", checklist.Title); err != nil {
				return err
			}
			for index, item := range checklist.Items {
				if _, err := fmt.Fprintf(writer, "  %d. %s\n", index+1, item); err != nil {
					return err
				}
			}
		}
	}

	if len(envelope.NextSteps) > 0 {
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(writer, "Next steps:"); err != nil {
			return err
		}
		for index, step := range envelope.NextSteps {
			if _, err := fmt.Fprintf(writer, "  %d. %s\n", index+1, step); err != nil {
				return err
			}
		}
	}

	return nil
}

func statusLabel(status preflight.Status) string {
	switch status {
	case preflight.StatusPass:
		return "PASS"
	case preflight.StatusWarn:
		return "WARN"
	case preflight.StatusFail:
		return "FAIL"
	case preflight.StatusManual:
		return "MANUAL"
	default:
		return "UNKNOWN"
	}
}
