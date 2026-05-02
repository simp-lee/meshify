package cli

import (
	"errors"
	"fmt"
	"io"
	"meshify/internal/output"
	"meshify/internal/verify"
	"os"
	"strings"
)

func newStatusCommand() command {
	return command{
		summary: "Show config readiness and persisted deploy context.",
		usage:   writeStatusHelp,
		run:     runStatus,
	}
}

func runStatus(ctx context, args []string) error {
	flagSet := newFlagSet("status")
	options := sharedOptions{configPath: DefaultConfigPath, formatValue: string(output.FormatHuman)}
	options.bind(flagSet, "Path to the meshify config file.")

	shown, err := parseFlags(flagSet, args, writeStatusHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse status flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("status", flagSet); err != nil {
		return err
	}

	formatter, err := options.formatter(ctx.stdout)
	if err != nil {
		return err
	}
	checkpointPath := checkpointPathForConfigFn(options.configPath)

	if _, err := os.Stat(options.configPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return formatter.Write(output.Response{
				Command: "status",
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
			Command: "status",
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
	statusFields := func(extra ...output.Field) []output.Field {
		fields := append(configFields(options.configPath, cfg),
			output.Field{Label: "minimum client version", Value: "Tailscale >= v" + verify.MinimumTailscaleClientVersion},
		)
		fields = append(fields, extra...)
		return fields
	}

	checkpoint, err := checkpointStoreForConfigFn(options.configPath).Load()
	if err != nil {
		return formatCheckpointLoadFailureWithFields(formatter, "status", options.configPath, checkpointPath, err, statusFields())
	}
	if checkpoint.HasDeployContext() && strings.TrimSpace(checkpoint.DesiredStateDigest) == "" {
		return formatter.Write(output.Response{
			Command: "status",
			Status:  "stale-deploy-context",
			Summary: "config is valid; persisted deploy context is missing its desired-state fingerprint",
			Fields: statusFields(
				output.Field{Label: "checkpoint path", Value: checkpointPath},
				output.Field{Label: "stale context", Value: "checkpoint data has no desired-state fingerprint; meshify will ignore that recovery data on the next deploy"},
			),
			NextSteps: []string{
				fmt.Sprintf("Use 'meshify deploy --config %s' to regenerate recovery state for the current runtime asset set.", options.configPath),
				fmt.Sprintf("Use 'meshify verify --config %s' to inspect runtime readiness.", options.configPath),
			},
		})
	}
	if checkpoint.HasDeployContext() && strings.TrimSpace(checkpoint.DesiredStateDigest) != "" {
		desiredStateDigest, err := deployDesiredStateDigest(cfg)
		if err != nil {
			failure := desiredStateDigestFailure("status", options.configPath, err)
			response := failure.Response("status")
			response.Fields = append(statusFields(), response.Fields...)
			if err := formatter.Write(response); err != nil {
				return err
			}
			return failure
		}
		if !checkpoint.MatchesDesiredState(desiredStateDigest) {
			return formatter.Write(output.Response{
				Command: "status",
				Status:  "stale-deploy-context",
				Summary: "config is valid; persisted deploy context is stale for the current desired state",
				Fields: statusFields(
					output.Field{Label: "checkpoint path", Value: checkpointPath},
					output.Field{Label: "stale context", Value: "config changed since the recorded deploy context was saved; meshify will ignore that recovery data on the next deploy"},
				),
				NextSteps: []string{
					fmt.Sprintf("Use 'meshify deploy --config %s' to record a fresh recovery point for the current runtime asset set.", options.configPath),
					fmt.Sprintf("Use 'meshify verify --config %s' to re-run runtime asset and onboarding readiness checks.", options.configPath),
				},
			})
		}
	}

	checkpointFields := []output.Field{{Label: "checkpoint path", Value: checkpointPath}}
	if checkpoint.CurrentCheckpoint != "" {
		checkpointFields = append(checkpointFields, output.Field{Label: "current checkpoint", Value: checkpoint.CurrentCheckpoint})
	}
	if len(checkpoint.CompletedCheckpoints) > 0 {
		checkpointFields = append(checkpointFields, output.Field{Label: "completed checkpoints", Value: strings.Join(checkpoint.CompletedCheckpoints, ", ")})
	}
	if len(checkpoint.ModifiedPaths) > 0 {
		checkpointFields = append(checkpointFields, output.Field{Label: "modified paths", Value: summarizeModifiedPaths(checkpoint.ModifiedPaths)})
	}
	if len(checkpoint.ActivationHistory) > 0 {
		checkpointFields = append(checkpointFields, output.Field{Label: "activation history", Value: joinActivations(checkpoint.ActivationHistory)})
	}
	if warnings := deferredCheckpointWarnings(checkpoint.CompletedCheckpoints); len(warnings) > 0 {
		checkpointFields = append(checkpointFields, output.Field{Label: "warnings", Value: strings.Join(warnings, "; ")})
	}

	if checkpoint.LastFailure != nil {
		response := checkpoint.LastFailure.Response("status")
		response.Status = "deploy-failed"
		response.Fields = append(statusFields(checkpointFields...), response.Fields...)
		return formatter.Write(response)
	}

	if checkpoint.CurrentCheckpoint != "" {
		return formatter.Write(output.Response{
			Command: "status",
			Status:  "deploy-checkpoint",
			Summary: "config is valid; resumable deploy checkpoint is available",
			Fields:  statusFields(checkpointFields...),
			NextSteps: []string{
				fmt.Sprintf("Use 'meshify deploy --config %s' to resume from the recorded host checkpoint.", options.configPath),
				fmt.Sprintf("Use 'meshify verify --config %s' to inspect runtime readiness.", options.configPath),
			},
		})
	}

	if len(checkpoint.CompletedCheckpoints) > 0 || len(checkpoint.ModifiedPaths) > 0 || len(checkpoint.ActivationHistory) > 0 {
		return formatter.Write(output.Response{
			Command: "status",
			Status:  "deploy-history",
			Summary: "config is valid; last deploy context is available",
			Fields:  statusFields(checkpointFields...),
			NextSteps: []string{
				fmt.Sprintf("Use 'meshify deploy --config %s' to apply the current runtime asset set again.", options.configPath),
				fmt.Sprintf("Use 'meshify verify --config %s' to inspect runtime readiness and client-version requirements.", options.configPath),
			},
		})
	}

	return formatter.Write(output.Response{
		Command: "status",
		Status:  "config-ready",
		Summary: "config file is present and valid; no persisted deploy context yet",
		Fields:  statusFields(checkpointFields...),
		NextSteps: []string{
			fmt.Sprintf("Use 'meshify deploy --config %s' to apply the current runtime asset set.", options.configPath),
			fmt.Sprintf("Use 'meshify verify --config %s' for runtime asset and onboarding readiness checks.", options.configPath),
		},
	})
}

func writeStatusHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Show config readiness and persisted deploy context.",
		"",
		"Usage:",
		"  meshify status [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the meshify config file.",
		"  --format string   Output format: human | json",
	)
}
