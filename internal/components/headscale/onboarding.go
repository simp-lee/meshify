package headscale

import (
	"context"
	"fmt"
	"meshify/internal/host"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultUserName             = "meshify"
	DefaultPreAuthKeyExpiration = 24 * time.Hour
)

var safeUserNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

type OnboardingOptions struct {
	UserName   string
	Reusable   bool
	Expiration time.Duration
}

type OnboardingPlan struct {
	UserName   string
	Reusable   bool
	Expiration time.Duration
}

type User struct {
	ID   string
	Name string
}

type Onboarding struct {
	executor host.Executor
}

func NewOnboarding(executor host.Executor) Onboarding {
	return Onboarding{executor: executor}
}

func NewOnboardingPlan(options OnboardingOptions) (OnboardingPlan, error) {
	userName := strings.TrimSpace(options.UserName)
	if userName == "" {
		userName = DefaultUserName
	}
	if !safeUserNamePattern.MatchString(userName) {
		return OnboardingPlan{}, fmt.Errorf("headscale user name %q must use letters, digits, dot, underscore, or dash and start with a letter or digit", userName)
	}

	expiration := options.Expiration
	if expiration == 0 {
		expiration = DefaultPreAuthKeyExpiration
	}
	if expiration < time.Hour {
		return OnboardingPlan{}, fmt.Errorf("preauthkey expiration must be at least 1h")
	}

	return OnboardingPlan{
		UserName:   userName,
		Reusable:   options.Reusable,
		Expiration: expiration,
	}, nil
}

func HeadscaleCommand(args ...string) host.Command {
	commandArgs := make([]string, 0, len(args)+2)
	commandArgs = append(commandArgs, "--config", ConfigPath)
	commandArgs = append(commandArgs, args...)
	return host.Command{Name: "headscale", Args: commandArgs}
}

func CreateUserCommand(userName string) host.Command {
	return HeadscaleCommand("users", "create", userName)
}

func ListUsersCommand() host.Command {
	return HeadscaleCommand("users", "list")
}

func CreatePreAuthKeyCommand(userID string, plan OnboardingPlan) host.Command {
	args := []string{"preauthkeys", "create", "--user", strings.TrimSpace(userID), "--expiration", durationForCLI(plan.Expiration)}
	if plan.Reusable {
		args = append(args, "--reusable")
	}
	return HeadscaleCommand(args...)
}

func (onboarding Onboarding) CreatePreAuthKey(ctx context.Context, plan OnboardingPlan) (string, []host.Result, error) {
	results := []host.Result{}
	createUserResult, err := onboarding.executor.Run(ctx, CreateUserCommand(plan.UserName))
	results = append(results, createUserResult)
	if err != nil && !commandLooksLikeExistingUser(createUserResult, err) {
		return "", results, err
	}

	listResult, err := onboarding.executor.Run(ctx, ListUsersCommand())
	results = append(results, listResult)
	if err != nil {
		return "", results, err
	}
	userID, err := FindUserID(listResult.Stdout, plan.UserName)
	if err != nil {
		return "", results, err
	}

	keyResult, err := onboarding.executor.Run(ctx, CreatePreAuthKeyCommand(userID, plan))
	results = append(results, keyResult)
	if err != nil {
		return "", results, err
	}
	key := strings.TrimSpace(keyResult.Stdout)
	if key == "" {
		return "", results, fmt.Errorf("headscale preauthkeys create returned an empty key")
	}
	return key, results, nil
}

func FindUserID(output string, userName string) (string, error) {
	userName = strings.TrimSpace(userName)
	for _, user := range ParseUsers(output) {
		if user.Name == userName {
			return user.ID, nil
		}
	}
	return "", fmt.Errorf("headscale user %q was not found in users list output", userName)
}

func ParseUsers(output string) []User {
	users := []User{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := splitTableLine(line)
		if len(fields) < 2 {
			continue
		}
		if strings.EqualFold(fields[0], "id") && strings.EqualFold(fields[1], "name") {
			continue
		}
		if !looksNumeric(fields[0]) {
			continue
		}
		users = append(users, User{ID: fields[0], Name: fields[1]})
	}
	return users
}

func splitTableLine(line string) []string {
	rawFields := strings.FieldsFunc(line, func(r rune) bool {
		return r == '|' || r == '\t'
	})
	fields := make([]string, 0, len(rawFields))
	for _, field := range rawFields {
		field = strings.TrimSpace(field)
		if field != "" {
			fields = append(fields, field)
		}
	}
	if len(fields) > 1 {
		return fields
	}
	return strings.Fields(line)
}

func commandLooksLikeExistingUser(result host.Result, err error) bool {
	text := strings.ToLower(strings.TrimSpace(result.Stdout + "\n" + result.Stderr + "\n" + err.Error()))
	return strings.Contains(text, "already") && strings.Contains(text, "exist")
}

func durationForCLI(duration time.Duration) string {
	if duration%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(duration/time.Hour))
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	return duration.String()
}

func looksNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
