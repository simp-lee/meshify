package cli

import (
	stdcontext "context"
	"fmt"
	"io"
	"lanpanel/internal/host"
	"lanpanel/internal/output"
	"strings"
	"time"
)

type commandProgress struct {
	Enabled  bool
	Command  string
	Detail   string
	Interval time.Duration
}

func runHostCommandWithProgress(ctx stdcontext.Context, executor host.Executor, command host.Command, writer io.Writer, format output.Format, progress commandProgress) (host.Result, error) {
	if !progress.Enabled || format != output.FormatHuman {
		return executor.Run(ctx, command)
	}

	if progress.Interval <= 0 {
		progress.Interval = time.Minute
	}
	commandName := strings.TrimSpace(progress.Command)
	if commandName == "" {
		commandName = "command"
	}
	detail := strings.TrimSpace(progress.Detail)
	if detail != "" {
		_, _ = fmt.Fprintf(writer, "lanpanel %s: %s\n", commandName, detail)
	}

	type outcome struct {
		result host.Result
		err    error
	}
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		result, err := executor.Run(ctx, command)
		done <- outcome{result: result, err: err}
	}()

	ticker := time.NewTicker(progress.Interval)
	defer ticker.Stop()
	for {
		select {
		case result := <-done:
			return result.result, result.err
		case <-ticker.C:
			_, _ = fmt.Fprintf(writer, "lanpanel %s: still waiting for certificate issuance; elapsed %s\n", commandName, roundElapsed(time.Since(started)))
		}
	}
}

func dns01CertificateProgress(command string, enabled bool) commandProgress {
	return commandProgress{
		Enabled:  enabled,
		Command:  command,
		Detail:   "DNS-01 certificate issuance started; DNS propagation can take several minutes, especially with Tencent Cloud DNSPod / EdgeOne. Do not interrupt while lego is waiting.",
		Interval: time.Minute,
	}
}

func roundElapsed(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration.Round(time.Second)
}
