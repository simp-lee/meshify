package cli

import (
	"errors"
	"fmt"
	"io"
	"meshify/internal/config"
	"meshify/internal/output"
	"meshify/internal/workflow"
	"os"
)

func newInitCommand() command {
	return command{
		summary: "Generate a guided config or write the example template.",
		usage:   writeInitHelp,
		run:     runInit,
	}
}

func runInit(ctx context, args []string) error {
	flagSet := newFlagSet("init")
	options := sharedOptions{configPath: DefaultConfigPath, formatValue: string(output.FormatHuman)}
	advanced := false
	example := false
	options.bind(flagSet, "Path to create the meshify config file.")
	flagSet.BoolVar(&advanced, "advanced", false, "Prompt for advanced settings too.")
	flagSet.BoolVar(&example, "example", false, "Write the example template without guided prompts.")

	shown, err := parseFlags(flagSet, args, writeInitHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse init flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("init", flagSet); err != nil {
		return err
	}

	if advanced && example {
		return fmt.Errorf("--advanced and --example cannot be used together")
	}

	format, err := output.ParseFormat(options.formatValue)
	if err != nil {
		return err
	}
	formatter := output.NewFormatter(ctx.stdout, format)

	if _, err := os.Stat(options.configPath); err == nil {
		return fmt.Errorf("config file already exists at %s", options.configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config file: %w", err)
	}

	promptWriter := ctx.stderr
	if promptWriter == nil {
		promptWriter = ctx.stdout
	}
	prompter := output.NewPrompter(ctx.stdin, promptWriter)

	if example {
		result := workflow.ExampleInitResult()
		if err := config.WriteExampleFile(options.configPath); err != nil {
			return err
		}
		return formatter.Write(result.Response(options.configPath))
	}

	if advanced && !prompter.Enabled() {
		return fmt.Errorf("guided init requires interactive input; rerun in a terminal or use --example")
	}

	if prompter.Enabled() {
		result, err := workflow.RunInit(prompter, workflow.InitOptions{Advanced: advanced})
		if err != nil {
			return fmt.Errorf("run guided init: %w", err)
		}
		if err := result.Config.WriteFile(options.configPath); err != nil {
			return err
		}
		return formatter.Write(result.Response(options.configPath))
	}

	result := workflow.ExampleInitResult()
	if err := config.WriteExampleFile(options.configPath); err != nil {
		return err
	}

	return formatter.Write(result.Response(options.configPath))
}

func writeInitHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Generate a guided config or write the example template.",
		"",
		"Usage:",
		"  meshify init [--config path] [--format human|json] [--advanced] [--example]",
		"",
		"Flags:",
		"  --config string   Path to create the meshify config file.",
		"  --format string   Output format: human | json",
		"  --advanced        Prompt for advanced settings too.",
		"  --example         Write the example template without guided prompts.",
	)
}
