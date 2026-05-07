package headscale

import (
	"context"
	"encoding/json"
	"fmt"
	"meshify/internal/host"
	"regexp"
	"strconv"
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

type cliUser struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
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
	return HeadscaleCommand("users", "list", "--output", "json")
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
	matches := []User{}
	for _, user := range ParseUsers(output) {
		if user.Name == userName {
			matches = append(matches, user)
		}
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, user := range matches {
			ids = append(ids, user.ID)
		}
		return "", fmt.Errorf("headscale user %q matched multiple user IDs: %s", userName, strings.Join(ids, ", "))
	}
	if len(matches) == 1 {
		return matches[0].ID, nil
	}
	return "", fmt.Errorf("headscale user %q was not found in users list output", userName)
}

func ParseUsers(output string) []User {
	if users, ok := parseUsersJSON(output); ok {
		return users
	}
	return parseUsersTable(output)
}

func parseUsersJSON(output string) ([]User, bool) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, false
	}

	var users []cliUser
	if err := json.Unmarshal([]byte(output), &users); err == nil {
		return convertCLIUsers(users), true
	}

	var wrapped struct {
		Users []cliUser `json:"users"`
	}
	if err := json.Unmarshal([]byte(output), &wrapped); err == nil && wrapped.Users != nil {
		return convertCLIUsers(wrapped.Users), true
	}

	return nil, false
}

func convertCLIUsers(cliUsers []cliUser) []User {
	users := make([]User, 0, len(cliUsers))
	for _, user := range cliUsers {
		name := strings.TrimSpace(user.Name)
		if user.ID == 0 || name == "" {
			continue
		}
		users = append(users, User{ID: strconv.FormatUint(user.ID, 10), Name: name})
	}
	return users
}

func parseUsersTable(output string) []User {
	users := []User{}
	nameColumn := 1
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := splitTableLine(line)
		if len(fields) < 2 {
			continue
		}
		if column, ok := userNameColumn(fields); ok {
			nameColumn = column
			continue
		}
		if !looksNumeric(fields[0]) || nameColumn >= len(fields) {
			continue
		}
		name := strings.TrimSpace(fields[nameColumn])
		if name == "" {
			continue
		}
		users = append(users, User{ID: fields[0], Name: name})
	}
	return users
}

func splitTableLine(line string) []string {
	if strings.Contains(line, "|") {
		rawFields := strings.Split(line, "|")
		fields := make([]string, 0, len(rawFields))
		for index, field := range rawFields {
			field = strings.TrimSpace(field)
			if field == "" && (index == 0 || index == len(rawFields)-1) {
				continue
			}
			fields = append(fields, field)
		}
		return fields
	}

	if strings.Contains(line, "\t") {
		rawFields := strings.Split(line, "\t")
		fields := make([]string, 0, len(rawFields))
		for _, field := range rawFields {
			fields = append(fields, strings.TrimSpace(field))
		}
		return fields
	}

	return strings.Fields(line)
}

func userNameColumn(fields []string) (int, bool) {
	if len(fields) == 0 || !strings.EqualFold(strings.TrimSpace(fields[0]), "id") {
		return 0, false
	}

	nameColumn := -1
	for index, field := range fields {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "username":
			return index, true
		case "name":
			if nameColumn == -1 {
				nameColumn = index
			}
		}
	}
	if nameColumn == -1 {
		return 0, false
	}
	return nameColumn, true
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
