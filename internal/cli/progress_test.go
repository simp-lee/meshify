package cli

import (
	"bytes"
	stdcontext "context"
	"meshify/internal/host"
	"meshify/internal/output"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingProgressRunner struct {
	started chan struct{}
	release chan struct{}
}

func (runner blockingProgressRunner) Run(_ stdcontext.Context, command host.Command) (host.Result, error) {
	close(runner.started)
	<-runner.release
	return host.Result{Command: command}, nil
}

type instantProgressRunner struct{}

func (instantProgressRunner) Run(_ stdcontext.Context, command host.Command) (host.Result, error) {
	return host.Result{Command: command}, nil
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func TestRunHostCommandWithProgressWritesHumanProgress(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	writer := &lockedBuffer{}
	executor := host.NewExecutor(blockingProgressRunner{started: started, release: release}, nil)

	done := make(chan error, 1)
	go func() {
		_, err := runHostCommandWithProgress(stdcontext.Background(), executor, host.Command{Name: "lego"}, writer, output.FormatHuman, commandProgress{
			Enabled:  true,
			Command:  "deploy",
			Detail:   "DNS-01 certificate issuance started",
			Interval: time.Millisecond,
		})
		done <- err
	}()

	<-started
	waitForProgressOutput(t, writer, "still waiting for certificate issuance")
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runHostCommandWithProgress() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runHostCommandWithProgress() did not return after command completed")
	}

	text := writer.String()
	if !strings.Contains(text, "meshify deploy: DNS-01 certificate issuance started") {
		t.Fatalf("progress output = %q, want start message", text)
	}
	if !strings.Contains(text, "meshify deploy: still waiting for certificate issuance") {
		t.Fatalf("progress output = %q, want periodic wait message", text)
	}
}

func TestRunHostCommandWithProgressDoesNotWriteJSONProgress(t *testing.T) {
	t.Parallel()

	var writer bytes.Buffer
	executor := host.NewExecutor(instantProgressRunner{}, nil)
	if _, err := runHostCommandWithProgress(stdcontext.Background(), executor, host.Command{Name: "lego"}, &writer, output.FormatJSON, commandProgress{
		Enabled:  true,
		Command:  "deploy",
		Detail:   "DNS-01 certificate issuance started",
		Interval: time.Millisecond,
	}); err != nil {
		t.Fatalf("runHostCommandWithProgress() error = %v", err)
	}
	if writer.String() != "" {
		t.Fatalf("progress output = %q, want empty for JSON format", writer.String())
	}
}

func waitForProgressOutput(t *testing.T, writer *lockedBuffer, want string) {
	t.Helper()

	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("progress output = %q, want substring %q", writer.String(), want)
		case <-ticker.C:
			if strings.Contains(writer.String(), want) {
				return
			}
		}
	}
}
