package headscale

import (
	"context"
	"errors"
	"meshify/internal/host"
	"strings"
	"testing"
	"time"
)

func TestNewOnboardingPlanDefaultsAndValidatesUserName(t *testing.T) {
	t.Parallel()

	plan, err := NewOnboardingPlan(OnboardingOptions{})
	if err != nil {
		t.Fatalf("NewOnboardingPlan() error = %v", err)
	}
	if plan.UserName != DefaultUserName {
		t.Fatalf("UserName = %q, want %q", plan.UserName, DefaultUserName)
	}
	if plan.Expiration != DefaultPreAuthKeyExpiration {
		t.Fatalf("Expiration = %v, want %v", plan.Expiration, DefaultPreAuthKeyExpiration)
	}

	if _, err := NewOnboardingPlan(OnboardingOptions{UserName: "../bad"}); err == nil {
		t.Fatal("NewOnboardingPlan() error = nil, want invalid user name failure")
	}
	if _, err := NewOnboardingPlan(OnboardingOptions{Expiration: 30 * time.Minute}); err == nil {
		t.Fatal("NewOnboardingPlan() error = nil, want short expiration failure")
	}
}

func TestHeadscaleCommandsUseLocalConfigPath(t *testing.T) {
	t.Parallel()

	create := CreateUserCommand("meshify")
	if create.Name != "headscale" {
		t.Fatalf("Name = %q, want headscale", create.Name)
	}
	if got := strings.Join(create.Args, " "); got != "--config /etc/headscale/config.yaml users create meshify" {
		t.Fatalf("Args = %q", got)
	}

	plan, err := NewOnboardingPlan(OnboardingOptions{Reusable: true, Expiration: 48 * time.Hour})
	if err != nil {
		t.Fatalf("NewOnboardingPlan() error = %v", err)
	}
	keyCommand := CreatePreAuthKeyCommand("2", plan)
	if got := strings.Join(keyCommand.Args, " "); got != "--config /etc/headscale/config.yaml preauthkeys create --user 2 --expiration 48h --reusable" {
		t.Fatalf("Args = %q", got)
	}
}

func TestParseUsersFindsNumericUserID(t *testing.T) {
	t.Parallel()

	output := `
ID | Name    | Created
1  | admin   | 2026-01-01
2  | meshify | 2026-01-02
`
	userID, err := FindUserID(output, "meshify")
	if err != nil {
		t.Fatalf("FindUserID() error = %v", err)
	}
	if userID != "2" {
		t.Fatalf("userID = %q, want 2", userID)
	}
}

func TestOnboardingCreatePreAuthKeyUsesUserIDAndReturnsKey(t *testing.T) {
	t.Parallel()

	plan, err := NewOnboardingPlan(OnboardingOptions{UserName: "meshify", Reusable: true})
	if err != nil {
		t.Fatalf("NewOnboardingPlan() error = %v", err)
	}
	runner := &scriptedRunner{
		results: []host.Result{
			{},
			{Stdout: "ID | Name\n2 | meshify\n"},
			{Stdout: "hskey-auth-example\n"},
		},
	}
	onboarding := NewOnboarding(host.NewExecutor(runner, nil))

	key, results, err := onboarding.CreatePreAuthKey(context.Background(), plan)
	if err != nil {
		t.Fatalf("CreatePreAuthKey() error = %v", err)
	}
	if key != "hskey-auth-example" {
		t.Fatalf("key = %q", key)
	}
	if len(results) != 3 || len(runner.commands) != 3 {
		t.Fatalf("results = %d commands = %d, want 3", len(results), len(runner.commands))
	}
	if got := strings.Join(runner.commands[2].Args, " "); !strings.Contains(got, "preauthkeys create --user 2") {
		t.Fatalf("preauth command args = %q", got)
	}
}

func TestOnboardingIgnoresExistingUserCreateFailure(t *testing.T) {
	t.Parallel()

	plan, err := NewOnboardingPlan(OnboardingOptions{UserName: "meshify"})
	if err != nil {
		t.Fatalf("NewOnboardingPlan() error = %v", err)
	}
	runner := &scriptedRunner{
		results: []host.Result{
			{Stderr: "user already exists"},
			{Stdout: "ID | Name\n2 | meshify\n"},
			{Stdout: "hskey-auth-example\n"},
		},
		errors: map[int]error{0: errors.New("exit status 1")},
	}
	onboarding := NewOnboarding(host.NewExecutor(runner, nil))

	if _, _, err := onboarding.CreatePreAuthKey(context.Background(), plan); err != nil {
		t.Fatalf("CreatePreAuthKey() error = %v", err)
	}
}

type scriptedRunner struct {
	commands []host.Command
	results  []host.Result
	errors   map[int]error
}

func (runner *scriptedRunner) Run(_ context.Context, command host.Command) (host.Result, error) {
	index := len(runner.commands)
	runner.commands = append(runner.commands, command)

	result := host.Result{Command: command}
	if index < len(runner.results) {
		result = runner.results[index]
		result.Command = command
	}
	if runner.errors != nil {
		if err := runner.errors[index]; err != nil {
			return result, err
		}
	}
	return result, nil
}
