package cli

import (
	"errors"
	"fmt"
	"io"
	"meshify/internal/output"
	"meshify/internal/render"
	"meshify/internal/verify"
	"os"
)

func newVerifyCommand() command {
	return command{
		summary: "Validate config, runtime assets, and onboarding readiness.",
		usage:   writeVerifyHelp,
		run:     runVerify,
	}
}

func runVerify(ctx context, args []string) error {
	flagSet := newFlagSet("verify")
	options := sharedOptions{configPath: DefaultConfigPath, formatValue: string(output.FormatHuman)}
	options.bind(flagSet, "Path to the meshify config file.")

	shown, err := parseFlags(flagSet, args, writeVerifyHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse verify flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("verify", flagSet); err != nil {
		return err
	}

	formatter, err := options.formatter(ctx.stdout)
	if err != nil {
		return err
	}

	if _, err := os.Stat(options.configPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return formatter.Write(output.Response{
				Command: "verify",
				Status:  "missing-config",
				Summary: "no config file found",
				Fields: []output.Field{
					{Label: "config path", Value: options.configPath},
					{Label: "happy path", Value: "init -> deploy -> verify"},
				},
				NextSteps: []string{
					fmt.Sprintf("Run 'meshify init --config %s' to generate a starter config.", options.configPath),
				},
			})
		}
		return fmt.Errorf("stat config file: %w", err)
	}

	cfg, err := loadConfig(options.configPath)
	if err != nil {
		return formatter.Write(output.Response{
			Command: "verify",
			Status:  "invalid-config",
			Summary: "config file exists but failed validation",
			Fields: []output.Field{
				{Label: "config path", Value: options.configPath},
				{Label: "details", Value: err.Error()},
			},
			NextSteps: []string{
				fmt.Sprintf("Fix the config at %s and rerun 'meshify verify --config %s'.", options.configPath, options.configPath),
			},
		})
	}

	staged, err := render.StageRuntime(cfg)
	if err != nil {
		return formatter.Write(output.Response{
			Command: "verify",
			Status:  "failed",
			Summary: "runtime asset rendering failed",
			Fields: append(configFields(options.configPath, cfg),
				output.Field{Label: "details", Value: err.Error()},
			),
			NextSteps: []string{fmt.Sprintf("Fix config/template inputs and rerun 'meshify verify --config %s'.", options.configPath)},
		})
	}
	report := verify.StaticReport(cfg, staged)
	fields := append(configFields(options.configPath, cfg),
		output.Field{Label: "checks", Value: verify.SummarizeChecks(report.Checks)},
		output.Field{Label: "minimum client version", Value: "Tailscale >= v" + verify.MinimumTailscaleClientVersion},
	)
	fields = append(fields, verifyCheckFields(report.Checks)...)
	status := "passed"
	if report.Status() == verify.StatusFail {
		status = "failed"
	}

	nextSteps := []string{
		fmt.Sprintf("Use 'meshify deploy --config %s' to apply or refresh server runtime state.", options.configPath),
		"After deploy, join at least two clients from different networks and observe direct or DERP fallback paths.",
	}
	return formatter.Write(output.Response{
		Command:   "verify",
		Status:    status,
		Summary:   report.Summary(),
		Fields:    fields,
		NextSteps: nextSteps,
	})
}

func writeVerifyHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Validate config, runtime assets, and onboarding readiness.",
		"",
		"Usage:",
		"  meshify verify [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the meshify config file.",
		"  --format string   Output format: human | json",
	)
}

func verifyCheckFields(checks []verify.Check) []output.Field {
	fields := make([]output.Field, 0, len(checks))
	for _, check := range checks {
		fields = append(fields, output.Field{
			Label: "check " + check.ID,
			Value: string(check.Status) + ": " + check.Summary,
		})
	}
	return fields
}
