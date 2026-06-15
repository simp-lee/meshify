package cli

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"lanpanel/internal/acme"
	"lanpanel/internal/appconfig"
	"lanpanel/internal/apppreflight"
	"lanpanel/internal/apprender"
	"lanpanel/internal/appverify"
	"lanpanel/internal/components/appsvc"
	"lanpanel/internal/components/headscale"
	legocomponent "lanpanel/internal/components/lego"
	nginxcomponent "lanpanel/internal/components/nginx"
	tailscalecomponent "lanpanel/internal/components/tailscale"
	"lanpanel/internal/config"
	"lanpanel/internal/host"
	"lanpanel/internal/output"
	"lanpanel/internal/preflight"
	"lanpanel/internal/realip"
	"lanpanel/internal/realip/edgeone"
	"lanpanel/internal/realipassets"
	"lanpanel/internal/realiprender"
	"lanpanel/internal/render"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const DefaultAppConfigPath = appconfig.DefaultConfigPath

const minimumNginxHTTP2DirectiveVersion = "1.25.1"

var realIPLockDir = "/run/lanpanel"

var nginxVersionPattern = regexp.MustCompile(`nginx/([0-9]+)\.([0-9]+)\.([0-9]+)`)

var appNonPublicRoutableIPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

type appCommand struct {
	summary string
	usage   func(io.Writer) error
	run     func(context, []string) error
}

type appOptions struct {
	configPath  string
	formatValue string
}

type appStagedFileInstaller interface {
	Install(files []render.StagedFile) ([]host.FileInstallResult, error)
}

type appDeployEffects struct {
	modifiedPaths []string
	actions       []string
}

func (effects *appDeployEffects) AddPaths(paths ...string) {
	seen := make(map[string]struct{}, len(effects.modifiedPaths)+len(paths))
	for _, path := range effects.modifiedPaths {
		seen[path] = struct{}{}
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		effects.modifiedPaths = append(effects.modifiedPaths, path)
	}
}

func (effects *appDeployEffects) AddActions(actions ...string) {
	seen := make(map[string]struct{}, len(effects.actions)+len(actions))
	for _, action := range effects.actions {
		seen[action] = struct{}{}
	}
	for _, action := range actions {
		action = strings.TrimSpace(action)
		if action == "" {
			continue
		}
		if _, ok := seen[action]; ok {
			continue
		}
		seen[action] = struct{}{}
		effects.actions = append(effects.actions, action)
	}
}

func (effects *appDeployEffects) AddTailscaleResult(result tailscalecomponent.EnsureResult) {
	if strings.TrimSpace(result.SkippedReason) != "" {
		effects.AddActions("tailscale skipped: " + result.SkippedReason)
	}
	for _, command := range result.CommandsRun {
		effects.AddActions("tailscale: " + command)
		switch {
		case strings.Contains(command, "install-tailscale-keyring"):
			effects.AddPaths("/usr/share/keyrings/tailscale-archive-keyring.gpg", "/usr/share/keyrings/tailscale-archive-keyring.gpg.lanpanel-managed")
		case strings.Contains(command, "install-tailscale-apt-source"):
			effects.AddPaths("/etc/apt/sources.list.d/tailscale.list", "/etc/apt/sources.list.d/tailscale.list.lanpanel-managed")
		case strings.Contains(command, "install-tailscale-marker"):
			effects.AddPaths(tailscalecomponent.MarkerPath)
		}
	}
}

func (effects appDeployEffects) Fields() []output.Field {
	fields := []output.Field{{Label: "modified paths", Value: summarizeAppModifiedPaths(effects.modifiedPaths)}}
	if len(effects.actions) > 0 {
		fields = append(fields, output.Field{Label: "host actions", Value: strings.Join(effects.actions, ", ")})
	}
	return fields
}

func summarizeAppModifiedPaths(paths []string) string {
	if len(paths) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d total: %s", len(paths), strings.Join(paths, ", "))
}

var (
	appCommands = map[string]appCommand{
		"deploy": {summary: "Deploy a same-host service or tailnet upstream from app config.", usage: writeAppDeployHelp, run: runAppDeploy},
		"init":   {summary: "Generate an editable app example config.", usage: writeAppInitHelp, run: runAppInit},
		"realip": {summary: "Manage app real client IP profile runtime artifacts.", usage: writeAppRealIPHelp, run: runAppRealIP},
		"verify": {summary: "Validate app config and runtime templates.", usage: writeAppVerifyHelp, run: runAppVerify},
	}

	stageAppRuntimeFilesFn               = apprender.StageRuntime
	statAppServiceBinaryFn               = os.Stat
	lstatAppServicePathFn                = os.Lstat
	readAppServicePathFn                 = os.ReadFile
	detectAppDNSFn                       = detectAppDNS
	detectAppCurrentPublicIPsFn          = detectAppCurrentPublicIPs
	detectAppPortBindingsFn              = detectAppPortBindings
	detectAppListenPortStateFn           = detectAppListenPortState
	detectAppDNSCredentialStateFn        = detectAppDNSCredentialState
	detectAppServiceEnvFileStateFn       = detectAppServiceEnvFileState
	detectAppTailscaleAuthKeyFileStateFn = detectAppTailscaleAuthKeyFileState
	detectAppGoAccessAuthFileStateFn     = detectAppGoAccessAuthFileState
	detectAppGoAccessPortStateFn         = detectAppGoAccessPortState
	detectAppGoAccessAppListenBlockersFn = detectAppGoAccessAppListenBlockers
	detectAppGoAccessAppPortBlockersFn   = detectAppGoAccessAppPortBlockers
	detectAppGoAccessLocaleStateFn       = detectAppGoAccessLocaleState
	detectAppGoAccessLogFileStateFn      = detectAppGoAccessLogFileState
	isAppGoAccessManagedPortBindingFn    = isAppGoAccessManagedPortBinding
	isAppManagedPortBindingFn            = isAppManagedPortBinding
	readAppServiceUnitFileFn             = os.ReadFile
	readAppProcessCgroupFileFn           = os.ReadFile
	ensureAppNginxCompatibilityFn        = ensureAppNginxRuntimeCompatibility
	loadEdgeOneCredentialsFn             = edgeone.LoadCredentialsFromEnvFile
	describeEdgeOneOriginACLFn           = func(ctx stdcontext.Context, credentials edgeone.Credentials, zoneID string) (*edgeone.OriginACLInfo, error) {
		return edgeone.Client{}.DescribeOriginACL(ctx, credentials, zoneID)
	}
	stageRealIPRuntimeFilesFn      = realiprender.StageRuntime
	readDeployedRealIPProfileFn    = readDeployedRealIPProfile
	readDeployedRealIPStateFn      = readDeployedRealIPState
	readDeployedRealIPReferencesFn = readDeployedRealIPReferences
	lstatDeployedRealIPReferenceFn = os.Lstat
	lstatDeployedRealIPArtifactFn  = os.Lstat
	readDeployedRealIPArtifactFn   = os.ReadFile
	lstatRealIPCleanupPathFn       = os.Lstat
	acquireRealIPProfileLockFn     = acquireRealIPProfileLock
	realIPCleanupRoot              = "/var/lib/lanpanel/realip"
	currentExecutablePathFn        = os.Executable
	newAppFileInstallerFn          = func(executor host.Executor, privilege host.PrivilegeStrategy) appStagedFileInstaller {
		if privilege.RequiresSudo() {
			return host.NewFileInstaller(host.NewCommandFileSystem(executor), "")
		}
		return host.NewFileInstaller(nil, "")
	}
	newAppHostFileSystemFn = func(executor host.Executor, privilege host.PrivilegeStrategy) host.FileSystem {
		if privilege.RequiresSudo() {
			return host.NewCommandFileSystem(executor)
		}
		return host.OSFileSystem{}
	}
)

func newAppCommand() command {
	return command{
		summary: "Manage additional app deployments.",
		usage:   writeAppHelp,
		run:     runApp,
	}
}

func runApp(ctx context, args []string) error {
	if len(args) == 0 {
		return writeAppHelp(ctx.stdout)
	}
	switch args[0] {
	case "help", "-h", "--help":
		return runAppHelp(ctx, args[1:])
	}
	selected, ok := appCommands[args[0]]
	if !ok {
		if err := writeAppHelp(ctx.stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown app command %q", args[0])
	}
	return selected.run(ctx, args[1:])
}

func runAppHelp(ctx context, args []string) error {
	if len(args) == 0 {
		return writeAppHelp(ctx.stdout)
	}
	if args[0] == "realip" {
		return runAppRealIPHelp(ctx, args[1:])
	}
	selected, ok := appCommands[args[0]]
	if !ok {
		if err := writeAppHelp(ctx.stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown app command %q", args[0])
	}
	return selected.usage(ctx.stdout)
}

func runAppRealIP(ctx context, args []string) error {
	if len(args) == 0 {
		return writeAppRealIPHelp(ctx.stdout)
	}
	switch args[0] {
	case "help", "-h", "--help":
		return runAppRealIPHelp(ctx, args[1:])
	case "diagnostics":
		return runAppRealIPDiagnostics(ctx, args[1:])
	case "refresh":
		return runAppRealIPRefresh(ctx, args[1:])
	case "validate-reference":
		return runAppRealIPValidateReference(ctx, args[1:])
	default:
		if err := writeAppRealIPHelp(ctx.stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown app realip command %q", args[0])
	}
}

func runAppRealIPHelp(ctx context, args []string) error {
	if len(args) == 0 {
		return writeAppRealIPHelp(ctx.stdout)
	}
	switch args[0] {
	case "diagnostics":
		return writeAppRealIPDiagnosticsHelp(ctx.stdout)
	case "refresh":
		return writeAppRealIPRefreshHelp(ctx.stdout)
	case "validate-reference":
		return writeAppRealIPValidateReferenceHelp(ctx.stdout)
	default:
		if err := writeAppRealIPHelp(ctx.stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown app realip command %q", args[0])
	}
}

func runAppRealIPDiagnostics(ctx context, args []string) (returnErr error) {
	flagSet := newFlagSet("app realip diagnostics")
	var profileName string
	var formatValue string
	flagSet.StringVar(&profileName, "profile", "", "Name of the deployed realip profile to diagnose.")
	flagSet.StringVar(&formatValue, "format", string(output.FormatHuman), "Output format: human | json")
	shown, err := parseFlags(flagSet, args, writeAppRealIPDiagnosticsHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse app realip diagnostics flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("app realip diagnostics", flagSet); err != nil {
		return err
	}
	format, err := output.ParseFormat(formatValue)
	if err != nil {
		return err
	}
	formatter := output.NewFormatter(ctx.stdout, format)
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app realip diagnostics",
			Status:    "invalid-profile",
			Summary:   "--profile is required",
			NextSteps: []string{"Pass --profile with the deployed realip profile name."},
		})
	}
	permissions := detectPermissionStateFn()
	if !permissions.IsRoot {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsRootRequiredResponse(permissions))
	}
	names, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, "")
	if err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app realip diagnostics",
			Status:    "invalid-profile",
			Summary:   "Realip profile diagnostics failed",
			Fields:    []output.Field{{Label: "profile", Value: profileName}, {Label: "details", Value: err.Error()}},
			NextSteps: []string{"Pass a deployed EdgeOne realip profile name."},
		})
	}
	lock, err := acquireRealIPProfileLockFn(profileName)
	if err != nil {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsFailureResponse(profileName, names, err))
	}
	defer func() {
		if err := lock.Release(); err != nil {
			if returnErr != nil {
				returnErr = fmt.Errorf("%w; release EdgeOne realip profile lock failed: %v", returnErr, err)
				return
			}
			returnErr = err
		}
	}()
	privilege := deployPrivilegeStrategy(permissions)
	fileSystem := newAppHostFileSystemFn(newHostExecutorFn(nil).WithPrivilege(privilege), privilege)
	if err := guardRealIPProfileDirectories(fileSystem, names); err != nil {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsFailureResponse(profileName, names, err))
	}
	profile, err := readDeployedRealIPProfileFn(names.MetadataPath)
	if err != nil {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsFailureResponse(profileName, names, err))
	}
	state, err := readDeployedRealIPStateFn(names.StatePath, profileName)
	if err != nil {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsFailureResponse(profileName, names, err))
	}
	if err := ensureDeployedRealIPStateMatchesProfile(profile, state); err != nil {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsFailureResponse(profileName, names, err))
	}
	references, err := readDeployedRealIPReferencesFn(names.ReferenceDir)
	if err != nil {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsFailureResponse(profileName, names, err))
	}
	if len(references) == 0 {
		return writeAppFailureResponse(formatter, appRealIPDiagnosticsFailureResponse(profileName, names, fmt.Errorf("no deployed app references found for realip profile %s", profileName)))
	}

	fields := []output.Field{
		{Label: "profile", Value: profile.Name + " (" + profile.Provider + ")"},
		{Label: "zone id", Value: profile.ZoneID},
		{Label: "refresh interval", Value: profile.RefreshInterval},
		{Label: "edgeone env file", Value: profile.EnvFile},
		{Label: "deployed references", Value: summarizeRealIPReferences(references)},
	}
	fields = append(fields, realIPRuntimeFields(names, state, "deployed state read; run refresh to validate nginx -t and reload")...)
	failures := []string{}
	credentialStatus := "passed"
	if _, err := loadEdgeOneCredentialsFn(edgeone.OSFileSystem{}, profile.EnvFile); err != nil {
		credentialStatus = "failed: " + err.Error()
		failures = append(failures, "edgeone credential/env file: "+err.Error())
	}
	fields = append(fields, output.Field{Label: "edgeone credential/env file", Value: credentialStatus})
	marker := realIPManagedMarker(profileName, appconfig.RealIPProviderEdgeOne)
	for _, item := range []struct {
		label    string
		path     string
		validate func(string) error
		success  string
	}{
		{label: "realip nginx include status", path: names.NginxIncludePath, validate: func(path string) error {
			return validateDeployedRealIPNginxInclude(path, marker, state.TrustedCIDRs)
		}, success: ", CIDRs match state"},
		{label: "realip trusted CIDR include status", path: names.TrustedCIDRPath, validate: func(path string) error {
			return validateDeployedRealIPTrustedCIDRInclude(path, marker, state.TrustedCIDRs)
		}, success: ", CIDRs match state"},
		{label: "realip refresh service status", path: names.RefreshServicePath, validate: func(path string) error {
			return validateDeployedRealIPRefreshService(path, marker, profileName)
		}, success: ", command matches profile"},
		{label: "realip refresh timer status", path: names.RefreshTimerPath, validate: func(path string) error {
			return validateDeployedRealIPRefreshTimer(path, marker, profile)
		}, success: ", timer matches profile"},
	} {
		status, err := deployedRealIPRegularFileStatus(item.path)
		if err == nil && item.validate != nil {
			if validateErr := item.validate(item.path); validateErr != nil {
				status = "invalid: " + validateErr.Error()
				err = validateErr
			} else {
				status += item.success
			}
		}
		fields = append(fields, output.Field{Label: item.label, Value: status})
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		fields = append(fields, output.Field{Label: "details", Value: strings.Join(failures, "; ")})
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app realip diagnostics",
			Status:    "failed",
			Summary:   "Realip profile diagnostics failed",
			Fields:    fields,
			NextSteps: []string{"Fix the reported deployed realip profile, credential, or runtime file issue and rerun diagnostics."},
		})
	}
	return formatter.Write(output.Response{
		Command: "app realip diagnostics",
		Status:  "passed",
		Summary: "Realip profile diagnostics passed",
		Fields:  fields,
		NextSteps: []string{
			"Run sudo lanpanel app realip refresh --profile " + profileName + " after EdgeOne OriginACL changes.",
			"Keep cloud security group or host firewall allowlists aligned with current+next CIDRs.",
		},
	})
}

func runAppRealIPValidateReference(ctx context, args []string) error {
	flagSet := newFlagSet("app realip validate-reference")
	var profileName string
	var appName string
	var path string
	flagSet.StringVar(&profileName, "profile", "", "Expected deployed realip profile name.")
	flagSet.StringVar(&appName, "app", "", "Expected app name from the reference filename.")
	flagSet.StringVar(&path, "path", "", "Path to the deployed realip reference JSON.")
	shown, err := parseFlags(flagSet, args, writeAppRealIPValidateReferenceHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse app realip validate-reference flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("app realip validate-reference", flagSet); err != nil {
		return err
	}
	profileName = strings.TrimSpace(profileName)
	appName = strings.TrimSpace(appName)
	path = strings.TrimSpace(path)
	if profileName == "" || appName == "" || path == "" {
		return fmt.Errorf("app realip validate-reference requires --profile, --app, and --path")
	}
	if err := validateDeployedRealIPReferencePath(path, profileName, appName); err != nil {
		return err
	}
	info, err := lstatDeployedRealIPReferenceFn(path)
	if err != nil {
		return fmt.Errorf("stat deployed realip reference %s: %w", path, err)
	}
	if err := validateDeployedRealIPReferenceInfo(path, info); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read deployed realip reference %s: %w", path, err)
	}
	if _, err := parseDeployedRealIPReference(data, path, profileName, appName); err != nil {
		return err
	}
	return nil
}

func validateDeployedRealIPReferencePath(path string, profileName string, appName string) error {
	path = strings.TrimSpace(path)
	profileName = strings.TrimSpace(profileName)
	appName = strings.TrimSpace(appName)
	names, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, appName)
	if err != nil {
		return fmt.Errorf("deployed realip reference path identity is invalid: %w", err)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("deployed realip reference path must be a clean absolute path")
	}
	if path != names.ReferencePathForApp {
		return fmt.Errorf("deployed realip reference path %s must be %s", path, names.ReferencePathForApp)
	}
	return nil
}

func runAppRealIPRefresh(ctx context, args []string) error {
	flagSet := newFlagSet("app realip refresh")
	var profileName string
	var formatValue string
	flagSet.StringVar(&profileName, "profile", "", "Name of the deployed realip profile to refresh.")
	flagSet.StringVar(&formatValue, "format", string(output.FormatHuman), "Output format: human | json")
	shown, err := parseFlags(flagSet, args, writeAppRealIPRefreshHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse app realip refresh flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("app realip refresh", flagSet); err != nil {
		return err
	}
	format, err := output.ParseFormat(formatValue)
	if err != nil {
		return err
	}
	formatter := output.NewFormatter(ctx.stdout, format)
	if strings.TrimSpace(profileName) == "" {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app realip refresh",
			Status:    "invalid-profile",
			Summary:   "--profile is required",
			NextSteps: []string{"Pass --profile with the deployed realip profile name."},
		})
	}
	permissions := detectPermissionStateFn()
	if !permissions.IsRoot {
		return writeAppFailureResponse(formatter, appRealIPRefreshRootRequiredResponse(permissions))
	}
	effects := appDeployEffects{}
	state, profile, names, err := refreshDeployedRealIPProfile(stdcontext.Background(), strings.TrimSpace(profileName), &effects)
	if err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app realip refresh",
			Status:    "failed",
			Summary:   "Realip profile refresh failed",
			Fields:    appRealIPRefreshFailureFields(strings.TrimSpace(profileName), err),
			NextSteps: []string{"Fix the reported profile, credential, EdgeOne API, CIDR, or Nginx error and rerun the refresh command."},
		})
	}
	fields := []output.Field{
		{Label: "profile", Value: profile.Name + " (" + profile.Provider + ")"},
		{Label: "manual firewall confirmation", Value: "confirm cloud security group or host firewall allows only EdgeOne current+next origin ACL CIDRs to 80/443 before confirming any EdgeOne origin ACL update outside Lanpanel"},
	}
	fields = append(fields, realIPRuntimeFields(names, state, "nginx -t passed and Nginx reloaded")...)
	fields = append(fields, effects.Fields()...)
	return formatter.Write(output.Response{
		Command: "app realip refresh",
		Status:  "refreshed",
		Summary: "Realip profile refreshed from EdgeOne OriginACL",
		Fields:  fields,
		NextSteps: []string{
			"Run nginx -t and curl trusted/non-trusted request fixtures on the host if this is a production cutover.",
			"Update cloud security group or host firewall for current+next CIDRs; Lanpanel does not confirm EdgeOne origin ACL updates.",
		},
	})
}

func appRealIPRefreshFailureFields(profileName string, cause error) []output.Field {
	profileName = strings.TrimSpace(profileName)
	fields := []output.Field{{Label: "profile", Value: profileName}}
	names, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, "")
	if err != nil {
		fields = append(fields, output.Field{Label: "runtime paths", Value: "unavailable: " + err.Error()})
	} else {
		fields = append(fields,
			output.Field{Label: "active include", Value: names.NginxIncludePath},
			output.Field{Label: "trusted CIDRs", Value: names.TrustedCIDRPath},
			output.Field{Label: "state metadata", Value: names.StatePath},
			output.Field{Label: "profile metadata", Value: names.MetadataPath},
			output.Field{Label: "reference dir", Value: names.ReferenceDir},
			output.Field{Label: "refresh service", Value: names.RefreshServicePath},
			output.Field{Label: "refresh timer", Value: names.RefreshTimerPath},
		)
	}
	if cause != nil {
		fields = append(fields, output.Field{Label: "details", Value: cause.Error()})
	}
	return fields
}

func appRealIPDiagnosticsFailureResponse(profileName string, names appsvc.RealIPProfileNames, cause error) output.Response {
	fields := []output.Field{
		{Label: "profile", Value: strings.TrimSpace(profileName)},
		{Label: "active include", Value: names.NginxIncludePath},
		{Label: "trusted CIDRs", Value: names.TrustedCIDRPath},
		{Label: "state metadata", Value: names.StatePath},
		{Label: "profile metadata", Value: names.MetadataPath},
		{Label: "reference dir", Value: names.ReferenceDir},
		{Label: "refresh service", Value: names.RefreshServicePath},
		{Label: "refresh timer", Value: names.RefreshTimerPath},
	}
	if cause != nil {
		fields = append(fields, output.Field{Label: "details", Value: cause.Error()})
	}
	return output.Response{
		Command:   "app realip diagnostics",
		Status:    "failed",
		Summary:   "Realip profile diagnostics failed",
		Fields:    fields,
		NextSteps: []string{"Fix the reported deployed realip profile, state, reference, credential, or runtime file issue and rerun diagnostics."},
	}
}

func (options *appOptions) bind(flagSet interface {
	StringVar(*string, string, string, string)
}) {
	flagSet.StringVar(&options.configPath, "config", DefaultAppConfigPath, "Path to the lanpanel app config file.")
	flagSet.StringVar(&options.formatValue, "format", string(output.FormatHuman), "Output format: human | json")
}

func (options appOptions) formatter(stdout io.Writer) (output.Formatter, error) {
	format, err := output.ParseFormat(options.formatValue)
	if err != nil {
		return output.Formatter{}, err
	}
	return output.NewFormatter(stdout, format), nil
}

func runAppInit(ctx context, args []string) error {
	flagSet := newFlagSet("app init")
	options := appOptions{configPath: DefaultAppConfigPath, formatValue: string(output.FormatHuman)}
	options.bind(flagSet)
	shown, err := parseFlags(flagSet, args, writeAppInitHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse app init flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("app init", flagSet); err != nil {
		return err
	}
	formatter, err := options.formatter(ctx.stdout)
	if err != nil {
		return err
	}
	if _, err := os.Stat(options.configPath); err == nil {
		return fmt.Errorf("app config file already exists at %s", options.configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat app config file: %w", err)
	}
	if err := appconfig.WriteExampleFile(options.configPath); err != nil {
		return err
	}
	return formatter.Write(output.Response{
		Command: "app init",
		Status:  "created",
		Summary: "App example config written",
		Fields:  []output.Field{{Label: "config path", Value: options.configPath}},
		NextSteps: []string{
			"Edit app.domains, listen or upstream, and service settings.",
			fmt.Sprintf("Run 'sudo lanpanel app deploy --config %s' to deploy the app.", options.configPath),
		},
	})
}

func runAppVerify(ctx context, args []string) error {
	flagSet := newFlagSet("app verify")
	options := appOptions{configPath: DefaultAppConfigPath, formatValue: string(output.FormatHuman)}
	options.bind(flagSet)
	shown, err := parseFlags(flagSet, args, writeAppVerifyHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse app verify flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("app verify", flagSet); err != nil {
		return err
	}
	formatter, err := options.formatter(ctx.stdout)
	if err != nil {
		return err
	}
	cfg, response, ok := loadAppConfigForResponse(options.configPath, "app verify")
	if !ok {
		return writeAppFailureResponse(formatter, response)
	}
	if err := validateAppAgainstMainConfig(cfg); err != nil {
		return writeAppFailureResponse(formatter, appMainConfigConflictResponse(options.configPath, "app verify", err))
	}
	staged, err := stageAppRuntimeFilesFn(cfg)
	if err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app verify",
			Status:    "failed",
			Summary:   "App runtime template rendering failed",
			Fields:    []output.Field{{Label: "config path", Value: options.configPath}, {Label: "details", Value: err.Error()}},
			NextSteps: []string{"Fix app config or template inputs and rerun verify."},
		})
	}
	report := appverify.StaticReport(cfg, staged)
	status := "static-passed"
	if report.FailedCount() > 0 {
		status = "failed"
	}
	fields := appConfigFields(options.configPath, cfg)
	fields = append(fields, output.Field{Label: "verification scope", Value: "static-only: does not connect to the host or check deployed files, systemd, certificates, Nginx runtime, or Tailscale online state"})
	fields = append(fields, output.Field{Label: "checks", Value: appverify.SummarizeChecks(report.Checks)})
	for _, check := range report.Checks {
		fields = append(fields, output.Field{Label: "check " + check.ID, Value: string(check.Status) + ": " + check.Summary})
	}
	response = output.Response{
		Command: "app verify",
		Status:  status,
		Summary: report.Summary(),
		Fields:  fields,
		NextSteps: []string{
			fmt.Sprintf("Run 'sudo lanpanel app deploy --config %s' to apply or refresh app runtime files.", options.configPath),
			appRuntimeHostChecksStep(cfg),
		},
	}
	if report.FailedCount() > 0 {
		return writeAppFailureResponse(formatter, response)
	}
	return formatter.Write(response)
}

func runAppDeploy(ctx context, args []string) (returnErr error) {
	flagSet := newFlagSet("app deploy")
	options := appOptions{configPath: DefaultAppConfigPath, formatValue: string(output.FormatHuman)}
	options.bind(flagSet)
	shown, err := parseFlags(flagSet, args, writeAppDeployHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse app deploy flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("app deploy", flagSet); err != nil {
		return err
	}
	format, err := output.ParseFormat(options.formatValue)
	if err != nil {
		return err
	}
	formatter := output.NewFormatter(ctx.stdout, format)
	cfg, response, ok := loadAppConfigForResponse(options.configPath, "app deploy")
	if !ok {
		return writeAppFailureResponse(formatter, response)
	}
	if err := validateAppAgainstMainConfig(cfg); err != nil {
		return writeAppFailureResponse(formatter, appMainConfigConflictResponse(options.configPath, "app deploy", err))
	}

	effects := appDeployEffects{}
	permissions := detectPermissionStateFn()
	if !permissions.IsRoot {
		return writeAppFailureResponse(formatter, appDeployRootRequiredResponse(permissions))
	}
	dns := detectAppDNSFn(cfg)
	binaryOK, binaryPath := detectAppServiceBinary(cfg)
	appListenChecked, appListenReady, appListenDetail := detectAppListenPortStateFn(cfg)
	dnsCredentialsChecked, dnsCredentialsReady, dnsCredentialsDetail := detectAppDNSCredentialStateFn(cfg)
	serviceEnvFileChecked, serviceEnvFileReady, serviceEnvFileDetail := detectAppServiceEnvFileStateFn(cfg)
	authKeyFileChecked, authKeyFileReady, authKeyFileDetail := detectAppTailscaleAuthKeyFileStateFn(cfg)
	goAccessAuthChecked, goAccessAuthReady, goAccessAuthDetail := detectAppGoAccessAuthFileStateFn(cfg)
	goAccessPortChecked, goAccessPortReady, goAccessPortDetail := detectAppGoAccessPortStateFn(cfg)
	goAccessLocaleChecked, goAccessLocaleReady, goAccessLocaleDetail := detectAppGoAccessLocaleStateFn(cfg)
	goAccessLogChecked, goAccessLogReady, goAccessLogDetail := detectAppGoAccessLogFileStateFn(cfg)
	preflightReport := apppreflight.BuildReport(cfg, apppreflight.Inputs{
		Permissions:                 permissions,
		DNS:                         dns,
		Ports:                       detectAppPortBindingsFn(),
		AppListenChecked:            appListenChecked,
		AppListenReady:              appListenReady,
		AppListenDetail:             appListenDetail,
		ServiceBinaryOK:             binaryOK,
		ServiceBinaryPath:           binaryPath,
		ServiceEnvFile:              cfg.Service.EnvFile,
		ServiceEnvFileChecked:       serviceEnvFileChecked,
		ServiceEnvFileReady:         serviceEnvFileReady,
		ServiceEnvFileDetail:        serviceEnvFileDetail,
		TailscaleRequired:           cfg.RequiresTailscale(),
		TailscaleAuthKeyFile:        cfg.Tailscale.AuthKeyFile,
		TailscaleAuthKeyFileChecked: authKeyFileChecked,
		TailscaleAuthKeyFileReady:   authKeyFileReady,
		TailscaleAuthKeyFileDetail:  authKeyFileDetail,
		DNSCredentialsChecked:       dnsCredentialsChecked,
		DNSCredentialsReady:         dnsCredentialsReady,
		DNSCredentialsDetail:        dnsCredentialsDetail,
		GoAccessAuthFileChecked:     goAccessAuthChecked,
		GoAccessAuthFileReady:       goAccessAuthReady,
		GoAccessAuthFileDetail:      goAccessAuthDetail,
		GoAccessPortChecked:         goAccessPortChecked,
		GoAccessPortReady:           goAccessPortReady,
		GoAccessPortDetail:          goAccessPortDetail,
		GoAccessLocaleChecked:       goAccessLocaleChecked,
		GoAccessLocaleReady:         goAccessLocaleReady,
		GoAccessLocaleDetail:        goAccessLocaleDetail,
		GoAccessLogFileChecked:      goAccessLogChecked,
		GoAccessLogFileReady:        goAccessLogReady,
		GoAccessLogFileDetail:       goAccessLogDetail,
	})
	if preflightReport.FailedCount() > 0 {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app deploy",
			Status:    "blocked",
			Summary:   preflightReport.Summary(),
			Fields:    appPreflightFields(preflightReport),
			NextSteps: preflightReport.NextSteps(),
		})
	}

	names, err := appsvc.NewNames(cfg)
	if err != nil {
		return err
	}
	staged, err := stageAppRuntimeFilesFn(cfg)
	if err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command: "app deploy",
			Status:  "failed",
			Summary: "App runtime template rendering failed",
			Fields:  []output.Field{{Label: "details", Value: err.Error()}},
		})
	}
	staticReport := appverify.StaticReport(cfg, staged)
	if staticReport.FailedCount() > 0 {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app deploy",
			Status:    "failed",
			Summary:   staticReport.Summary(),
			Fields:    []output.Field{{Label: "checks", Value: appverify.SummarizeChecks(staticReport.Checks)}},
			NextSteps: []string{"Run 'lanpanel app verify' first, then fix the failed static checks."},
		})
	}

	executor := newHostExecutorFn(nil)
	privilege := deployPrivilegeStrategy(permissions)
	privilegedExecutor := executor.WithPrivilege(privilege)
	fileSystem := newAppHostFileSystemFn(privilegedExecutor, privilege)
	systemd := newHostSystemdFn(privilegedExecutor)
	var realIPLock realIPProfileLock
	realIPLockReleased := false
	releaseRealIPLock := func() error {
		if realIPLock == nil || realIPLockReleased {
			return nil
		}
		realIPLockReleased = true
		return realIPLock.Release()
	}
	defer func() {
		if returnErr == nil {
			return
		}
		if err := releaseRealIPLock(); err != nil {
			returnErr = fmt.Errorf("%w; release EdgeOne realip profile lock failed: %v", returnErr, err)
		}
	}()
	realIPPrepared := false
	var realIPNames appsvc.RealIPProfileNames
	var realIPState realip.State
	var realIPSharedStaged []realiprender.StagedFile
	var realIPReferenceStaged []realiprender.StagedFile
	if cfg.RealIPEnabled() {
		realIPLock, err = acquireRealIPProfileLockFn(strings.TrimSpace(cfg.Nginx.RealIPProfile))
		if err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to lock EdgeOne realip profile", err, effects)
		}
		preparedNames, preparedState, preparedStaged, err := prepareAppRealIPProfile(stdcontext.Background(), cfg, fileSystem)
		if err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to prepare EdgeOne realip profile", err, effects)
		}
		sharedStaged, referenceStaged, err := splitAppRealIPStagedFiles(preparedStaged, preparedNames.ReferencePathForApp)
		if err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to prepare EdgeOne realip profile", err, effects)
		}
		realIPPrepared = true
		realIPNames = preparedNames
		realIPState = preparedState
		realIPSharedStaged = sharedStaged
		realIPReferenceStaged = referenceStaged
	}
	if err := guardAppOwnership(fileSystem, cfg, staged); err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app deploy",
			Status:    "blocked",
			Summary:   "Target file exists and is not a Lanpanel-managed file for this app",
			Fields:    []output.Field{{Label: "details", Value: err.Error()}},
			NextSteps: []string{"Inspect the conflicting file, then migrate/delete it manually or change app.name."},
		})
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardEnabledSiteCommand(names)); err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app deploy",
			Status:    "blocked",
			Summary:   "Nginx enabled site exists and does not belong to this app",
			Fields:    []output.Field{{Label: "details", Value: err.Error()}},
			NextSteps: []string{"Inspect the conflicting Nginx enabled site, then migrate/delete it manually or change app.name."},
		})
	}

	if !cfg.Nginx.GoAccess.Enabled {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardManagedGoAccessRuntimeRemovalCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess stale runtime removal check failed", err, effects)
		}
		effects.AddActions("checked stale GoAccess runtime removal candidates")
	} else if !appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardManagedGoAccessLogrotateRemovalCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess stale logrotate removal check failed", err, effects)
		}
		effects.AddActions("checked stale GoAccess logrotate removal candidate")
	}

	goAccessPostCutoverCleanupDone := false
	var goAccessAppListenBlockers []preflight.PortBinding
	var goAccessAppPortBlockers []preflight.PortBinding
	cleanupStaleGoAccessPostCutover := func() (string, error) {
		if goAccessPostCutoverCleanupDone {
			return "", nil
		}
		goAccessPostCutoverCleanupDone = true
		if !cfg.Nginx.GoAccess.Enabled {
			removedPaths, err := removeStaleGoAccessRuntime(stdcontext.Background(), privilegedExecutor, names)
			effects.AddPaths(removedPaths...)
			if len(removedPaths) > 0 {
				effects.AddActions("removed stale GoAccess runtime")
			}
			if err != nil {
				return "Failed to remove stale GoAccess runtime", err
			}
			if hasString(removedPaths, "/etc/systemd/system/"+names.GoAccessServiceUnit) {
				if _, err := systemd.DaemonReload(stdcontext.Background()); err != nil {
					return "systemd daemon-reload failed", err
				}
				effects.AddActions("systemd daemon-reload")
			}
			return "", nil
		}
		if !appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
			removedPaths, err := removeStaleGoAccessLogrotate(stdcontext.Background(), privilegedExecutor, names)
			effects.AddPaths(removedPaths...)
			if len(removedPaths) > 0 {
				effects.AddActions("removed stale GoAccess logrotate")
			}
			if err != nil {
				return "Failed to remove stale GoAccess logrotate", err
			}
		}
		return "", nil
	}

	if cfg.Nginx.GoAccess.Enabled {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessAuthFileMetadataCommand(names, cfg.Nginx.GoAccess.AuthBasicUserFile)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess basic auth file metadata check failed", err, effects)
		}
		effects.AddActions("checked GoAccess basic auth file metadata")
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessSystemUserCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess system user/group conflict", err, effects)
		}
		effects.AddActions("checked GoAccess system user/group")
		if appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessLogDirectoryCommand(names)); err != nil {
				return appDeployFailureWithEffects(formatter, "GoAccess log directory ownership check failed", err, effects)
			}
			effects.AddActions("checked GoAccess log directory ownership")
		}
	}

	if cfg.RequiresTailscale() {
		tailscaleResult, err := ensureAppTailscale(stdcontext.Background(), cfg, privilegedExecutor)
		effects.AddTailscaleResult(tailscaleResult)
		if err != nil {
			return appDeployFailureWithEffects(formatter, "Tailscale client prerequisite failed", err, effects)
		}
	}

	if cfg.Mode() == appconfig.ModeListen {
		blockers, detected := detectAppGoAccessAppListenBlockersFn(cfg, names)
		if !detected {
			return appDeployFailureWithEffects(formatter, "GoAccess current listener check failed", fmt.Errorf("could not confirm current managed GoAccess listener state before app listener reuse"), effects)
		}
		goAccessAppListenBlockers = blockers
	}
	if cfg.Nginx.GoAccess.Enabled {
		blockers, detected := detectAppGoAccessAppPortBlockersFn(cfg, names)
		if !detected {
			return appDeployFailureWithEffects(formatter, "app current listener check failed", fmt.Errorf("could not confirm current managed app listener state before GoAccess listener reuse"), effects)
		}
		goAccessAppPortBlockers = blockers
	}

	if cfg.Nginx.GoAccess.Enabled && !appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
		for _, command := range appsvc.EnsureGoAccessSystemUserCommands(names) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return appDeployFailureWithEffects(formatter, "Failed to create or confirm GoAccess system user/group", err, effects)
			}
		}
		effects.AddActions("ensured GoAccess system user/group")
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessCanonicalLogReadableCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess canonical access log readability check failed", err, effects)
		}
		effects.AddActions("checked explicit GoAccess access log readability")
	}

	effects.AddActions("started app host dependency check/install")
	if err := ensureAppHostDependencies(stdcontext.Background(), cfg, privilegedExecutor); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to install app host dependencies", err, effects)
	}
	effects.AddActions("ensured app host dependencies")
	if cfg.Nginx.GoAccess.Enabled {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessAuthFileCommand(names, cfg.Nginx.GoAccess.AuthBasicUserFile)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess basic auth file check failed", err, effects)
		}
		effects.AddActions("checked GoAccess basic auth file")
		if appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
			for _, command := range appsvc.EnsureGoAccessSystemUserCommands(names) {
				if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
					return appDeployFailureWithEffects(formatter, "Failed to create or confirm GoAccess system user/group", err, effects)
				}
			}
			effects.AddActions("ensured GoAccess system user/group")
		}
	}
	if _, err := privilegedExecutor.Systemctl(stdcontext.Background(), "enable", "--now", "nginx.service"); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to start Nginx service", err, effects)
	}
	effects.AddActions("enabled and started Nginx service")
	if _, err := privilegedExecutor.Run(stdcontext.Background(), nginxcomponent.DisableDefaultSiteCommand()); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to disable Nginx default site", err, effects)
	}
	effects.AddPaths(nginxcomponent.DefaultSiteEnabledPath)
	effects.AddActions("disabled distro Nginx default site if present")
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardDefaultServerCommand(names)); err != nil {
		return appDeployFailureWithEffects(formatter, "Nginx default_server conflict", err, effects)
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardServerNameConflictsCommand(names, cfg.App.Domains)); err != nil {
		return appDeployFailureWithEffects(formatter, "Nginx server_name conflict", err, effects)
	}
	if realIPPrepared {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardRealIPConflictsCommand(names, realIPNames)); err != nil {
			return appDeployFailureWithEffects(formatter, "Nginx realip directive conflict", err, effects)
		}
		effects.AddActions("checked Nginx realip directive conflicts")
	}
	if err := ensureAppNginxCompatibilityFn(stdcontext.Background(), cfg, privilegedExecutor); err != nil {
		return appDeployFailureWithEffects(formatter, "Nginx runtime compatibility check failed", err, effects)
	}
	effects.AddActions("checked Nginx runtime compatibility")
	if cfg.Nginx.GoAccess.Enabled {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessWebSocketPortAssignmentCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess WebSocket port assignment conflict", err, effects)
		}
		effects.AddActions("checked GoAccess WebSocket port assignment")
	}
	if cfg.Mode() == appconfig.ModeListen {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardSystemUserCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "app system user/group conflict", err, effects)
		}
		effects.AddActions("checked app system user/group")
	}
	rootGuardCommand := appsvc.GuardRootDirectoriesCommand(names)
	if cfg.Nginx.GoAccess.Enabled {
		rootGuardCommand = appsvc.GuardRootDirectoriesWithGoAccessAuthBootstrapCommand(names, cfg.Nginx.GoAccess.AuthBasicUserFile)
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), rootGuardCommand); err != nil {
		return appDeployFailureWithEffects(formatter, "app root directory ownership check failed", err, effects)
	}
	effects.AddPaths(names.VarLibDir, names.VarLibMarkerPath, names.EtcDir, names.EtcMarkerPath, names.HookDir, names.HookDirMarkerPath)
	effects.AddActions("checked app root directory ownership")
	if cfg.Mode() == appconfig.ModeListen {
		for _, command := range appsvc.EnsureSystemUserCommands(names) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return appDeployFailureWithEffects(formatter, "Failed to create or confirm app system user/group", err, effects)
			}
		}
		effects.AddActions("ensured app system user/group")
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardServiceAccessCommand(names, cfg.ServiceBinary(), cfg.Service.WorkingDirectory)); err != nil {
			return appDeployFailureWithEffects(formatter, "app service user access check failed", err, effects)
		}
		effects.AddActions("checked app service user access")
	}
	if cfg.Nginx.GoAccess.Enabled {
		for _, command := range appsvc.EnsureGoAccessDirectoryCommands(names, appsvc.GoAccessManagesCanonicalAccessLog(cfg)) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return appDeployFailureWithEffects(formatter, "Failed to create GoAccess directories", err, effects)
			}
		}
		if appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardManagedGoAccessCanonicalLogReadableCommand(names)); err != nil {
				return appDeployFailureWithEffects(formatter, "GoAccess canonical access log readability check failed", err, effects)
			}
			effects.AddActions("checked managed GoAccess access log readability")
		}
		effects.AddPaths(names.GoAccessReportDir, names.GoAccessDBPath)
		if appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
			effects.AddPaths(names.GoAccessLogDir, names.GoAccessLogDirMarkerPath, names.GoAccessCanonicalAccessLogPath)
		}
	}
	for _, command := range appsvc.EnsureDirectoryCommands(names) {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to create app directories", err, effects)
		}
	}
	effects.AddPaths(names.WebrootPath, names.LegoDataPath, names.TLSDir)
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardTLSOwnershipCommand(names)); err != nil {
		return appDeployFailureWithEffects(formatter, "app TLS certificate directory ownership check failed", err, effects)
	}
	effects.AddPaths(names.TLSMarkerPath)

	realIPSharedInstalled := false
	realIPSharedCommitted := false
	realIPSharedSnapshotTaken := false
	var realIPSharedSnapshots []realIPFileSnapshot
	var realIPSharedDirectorySnapshots []realIPDirectorySnapshot
	installRealIPSharedArtifacts := func() (string, error) {
		if !realIPPrepared || realIPSharedInstalled {
			return "", nil
		}
		if err := guardRealIPProfileDirectories(fileSystem, realIPNames); err != nil {
			return "Failed to write EdgeOne realip profile artifacts", err
		}
		if len(realIPSharedStaged) == 0 {
			return "Failed to write EdgeOne realip profile artifacts", fmt.Errorf("no shared realip artifacts staged for profile %s", realIPNames.ProfileName)
		}
		if !realIPSharedSnapshotTaken {
			directorySnapshots, err := snapshotRealIPDirectories(fileSystem, realIPNames)
			if err != nil {
				return "Failed to write EdgeOne realip profile artifacts", err
			}
			snapshots, err := snapshotRealIPFiles(fileSystem, realIPSharedStaged, false)
			if err != nil {
				return "Failed to write EdgeOne realip profile artifacts", err
			}
			realIPSharedSnapshots = snapshots
			realIPSharedDirectorySnapshots = directorySnapshots
			realIPSharedSnapshotTaken = true
		}
		results, err := newAppFileInstallerFn(privilegedExecutor, privilege).Install(realiprender.ConvertStagedFiles(realIPSharedStaged))
		effects.AddPaths(host.CollectModifiedPaths(results)...)
		if err != nil {
			if rollbackErr := restoreRealIPDeploySnapshots(stdcontext.Background(), privilegedExecutor, fileSystem, realIPSharedSnapshots, realIPSharedDirectorySnapshots); rollbackErr != nil {
				return "Failed to write EdgeOne realip profile artifacts", fmt.Errorf("%w; rollback EdgeOne realip profile artifacts failed: %v", err, rollbackErr)
			}
			return "Failed to write EdgeOne realip profile artifacts", err
		}
		effects.AddActions("prepared EdgeOne realip profile " + realIPNames.ProfileName)
		realIPSharedInstalled = true
		return "", nil
	}
	var appNginxSnapshot appNginxSiteSnapshot
	appNginxSnapshotTaken := false
	realIPNginxReloadAttempted := false
	failAfterRealIPSharedInstall := func(summary string, err error) error {
		if realIPPrepared && realIPSharedInstalled && !realIPSharedCommitted {
			if appNginxSnapshotTaken {
				if rollbackErr := restoreAppNginxSite(stdcontext.Background(), privilegedExecutor, fileSystem, appNginxSnapshot); rollbackErr != nil {
					return appDeployFailureWithEffects(formatter, summary, fmt.Errorf("%w; rollback app Nginx site failed: %v", err, rollbackErr), effects)
				}
			}
			err = rollbackRealIPDeploy(stdcontext.Background(), privilegedExecutor, fileSystem, realIPSharedSnapshots, realIPSharedDirectorySnapshots, err, realIPNginxReloadAttempted)
			realIPSharedInstalled = false
		}
		return appDeployFailureWithEffects(formatter, summary, err, effects)
	}
	if summary, err := installRealIPSharedArtifacts(); err != nil {
		return appDeployFailureWithEffects(formatter, summary, err, effects)
	}

	if realIPPrepared {
		snapshot, err := snapshotAppNginxSite(fileSystem, names)
		if err != nil {
			return failAfterRealIPSharedInstall("Failed to write app runtime files", err)
		}
		appNginxSnapshot = snapshot
		appNginxSnapshotTaken = true
	}
	results, err := newAppFileInstallerFn(privilegedExecutor, privilege).Install(convertAppStagedFiles(staged))
	effects.AddPaths(host.CollectModifiedPaths(results)...)
	if err != nil {
		return failAfterRealIPSharedInstall("Failed to write app runtime files", err)
	}
	realIPReferenceInstalled := false
	var realIPReferenceSnapshot appRealIPReferenceSnapshot
	realIPReferenceSnapshotTaken := false
	installRealIPReferenceBeforeNginxReload := func() (string, error) {
		if !realIPPrepared || realIPReferenceInstalled {
			return "", nil
		}
		if err := guardRealIPProfileDirectories(fileSystem, realIPNames); err != nil {
			return "Failed to write app realip reference", err
		}
		if len(realIPReferenceStaged) == 0 {
			return "Failed to write app realip reference", fmt.Errorf("no app realip reference staged for profile %s", realIPNames.ProfileName)
		}
		if !realIPReferenceSnapshotTaken {
			snapshot, err := snapshotAppRealIPReference(fileSystem, realIPNames.ReferencePathForApp)
			if err != nil {
				return "Failed to write app realip reference", err
			}
			realIPReferenceSnapshot = snapshot
			realIPReferenceSnapshotTaken = true
		}
		results, err := newAppFileInstallerFn(privilegedExecutor, privilege).Install(realiprender.ConvertStagedFiles(realIPReferenceStaged))
		effects.AddPaths(host.CollectModifiedPaths(results)...)
		if err != nil {
			if rollbackErr := restoreAppRealIPReference(stdcontext.Background(), privilegedExecutor, fileSystem, realIPReferenceSnapshot); rollbackErr != nil {
				return "Failed to write app realip reference", fmt.Errorf("%w; rollback app realip reference failed: %v", err, rollbackErr)
			}
			return "Failed to write app realip reference", err
		}
		effects.AddActions("registered app realip reference")
		realIPReferenceInstalled = true
		return "", nil
	}
	if cfg.Nginx.GoAccess.Enabled {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessRuntimeAccessCommand(names)); err != nil {
			return failAfterRealIPSharedInstall("GoAccess runtime permission check failed", err)
		}
		effects.AddActions("checked GoAccess runtime permissions")
	}
	if _, err := systemd.DaemonReload(stdcontext.Background()); err != nil {
		return failAfterRealIPSharedInstall("systemd daemon-reload failed", err)
	}
	effects.AddActions("systemd daemon-reload")

	appServicePreparedBeforeListenerCutover := false
	appServiceRemovedBeforeListenerCutover := false
	startAppServiceBeforeListenerCutover := func(restartAction string) (string, error) {
		if appServicePreparedBeforeListenerCutover {
			return "", nil
		}
		if _, err := systemd.Enable(stdcontext.Background(), names.ServiceUnit); err != nil {
			return "Failed to enable app service before listener reuse", err
		}
		effects.AddActions("enabled app systemd service")
		if _, err := systemd.Restart(stdcontext.Background(), names.ServiceUnit); err != nil {
			return "Failed to restart app service before listener reuse", err
		}
		effects.AddActions(restartAction)
		appServicePreparedBeforeListenerCutover = true
		return "", nil
	}
	removeAppServiceBeforeListenerCutover := func() (string, error) {
		if appServiceRemovedBeforeListenerCutover {
			return "", nil
		}
		removedPaths, err := removeStaleAppServiceUnit(stdcontext.Background(), privilegedExecutor, names)
		effects.AddPaths(removedPaths...)
		if len(removedPaths) > 0 {
			effects.AddActions("removed stale app systemd service before GoAccess listener reuse")
		}
		if err != nil {
			return "Failed to remove stale app service before GoAccess listener reuse", err
		}
		if _, err := systemd.DaemonReload(stdcontext.Background()); err != nil {
			return "systemd daemon-reload failed", err
		}
		effects.AddActions("systemd daemon-reload")
		appServiceRemovedBeforeListenerCutover = true
		return "", nil
	}
	clearGoAccessAppListenBlockersBeforeNginxReload := func() (string, error) {
		if len(goAccessAppListenBlockers) == 0 {
			return "", nil
		}
		if _, err := systemd.Stop(stdcontext.Background(), names.GoAccessServiceUnit); err != nil {
			return "Failed to stop current GoAccess service before app listener reuse", err
		}
		effects.AddActions("stopped GoAccess systemd service before app listener reuse")
		return startAppServiceBeforeListenerCutover("restarted app systemd service before app listener reuse")
	}
	clearGoAccessAppPortBlockersBeforeNginxReload := func() (string, error) {
		if len(goAccessAppPortBlockers) == 0 || appServicePreparedBeforeListenerCutover || appServiceRemovedBeforeListenerCutover {
			return "", nil
		}
		switch cfg.Mode() {
		case appconfig.ModeListen:
			return startAppServiceBeforeListenerCutover("restarted app systemd service before GoAccess listener reuse")
		case appconfig.ModeUpstream:
			return removeAppServiceBeforeListenerCutover()
		}
		return "", nil
	}
	activateAppNginxBeforeReload := func() (string, error) {
		if summary, err := installRealIPSharedArtifacts(); err != nil {
			return summary, err
		}
		if summary, err := installRealIPReferenceBeforeNginxReload(); err != nil {
			return summary, err
		}
		rollbackReferenceFailure := func(summary string, err error) (string, error) {
			if !realIPReferenceSnapshotTaken || !realIPReferenceInstalled {
				return summary, err
			}
			if rollbackErr := restoreAppRealIPReference(stdcontext.Background(), privilegedExecutor, fileSystem, realIPReferenceSnapshot); rollbackErr != nil {
				return summary, fmt.Errorf("%w; rollback app realip reference failed: %v", err, rollbackErr)
			}
			realIPReferenceInstalled = false
			return summary, err
		}
		if err := prepareAppNginxActivation(stdcontext.Background(), privilegedExecutor, names, &effects); err != nil {
			return rollbackReferenceFailure("Failed to enable app Nginx site", err)
		}
		if summary, err := clearGoAccessAppListenBlockersBeforeNginxReload(); err != nil {
			return rollbackReferenceFailure(summary, err)
		}
		if summary, err := clearGoAccessAppPortBlockersBeforeNginxReload(); err != nil {
			return rollbackReferenceFailure(summary, err)
		}
		if realIPPrepared {
			realIPNginxReloadAttempted = true
		}
		if err := reloadAppNginxTracking(stdcontext.Background(), privilegedExecutor, &effects); err != nil {
			return rollbackReferenceFailure("Failed to enable app Nginx site", err)
		}
		if realIPPrepared {
			realIPSharedCommitted = true
		}
		return "", nil
	}

	if cfg.App.ACMEChallenge == appconfig.ACMEChallengeHTTP01 {
		for _, command := range appsvc.HTTP01BootstrapCommands(names) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return failAfterRealIPSharedInstall("Failed to prepare HTTP-01 bootstrap certificate", err)
			}
		}
		effects.AddPaths(names.WebrootPath, names.LegoDataPath, names.TLSDir, names.FullchainPath, names.PrivateKeyPath)
		effects.AddActions("prepared HTTP-01 bootstrap certificate")
		if summary, err := activateAppNginxBeforeReload(); err != nil {
			return failAfterRealIPSharedInstall(summary, err)
		}
		if summary, err := cleanupStaleGoAccessPostCutover(); err != nil {
			return failAfterRealIPSharedInstall(summary, err)
		}
	}
	certPlan, err := appsvc.NewCertificatePlan(cfg, names)
	if err != nil {
		return failAfterRealIPSharedInstall("Failed to build app TLS certificate plan", err)
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), legocomponent.MigrationGateCommand(names.LegoDataPath)); err != nil {
		return failAfterRealIPSharedInstall("Failed to migrate app lego v5 storage", err)
	}
	if _, err := runHostCommandWithProgress(
		stdcontext.Background(),
		privilegedExecutor,
		certPlan.Command,
		ctx.stdout,
		format,
		dns01CertificateProgress("app deploy", cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01),
	); err != nil {
		return failAfterRealIPSharedInstall("Failed to issue app TLS certificate", err)
	}
	effects.AddPaths(names.LegoDataPath, names.FullchainPath, names.PrivateKeyPath)
	effects.AddActions("issued or renewed app certificate")
	if cfg.App.ACMEChallenge != appconfig.ACMEChallengeHTTP01 {
		if summary, err := activateAppNginxBeforeReload(); err != nil {
			return failAfterRealIPSharedInstall(summary, err)
		}
		if summary, err := cleanupStaleGoAccessPostCutover(); err != nil {
			return failAfterRealIPSharedInstall(summary, err)
		}
	}

	if cfg.Mode() == appconfig.ModeUpstream && !appServiceRemovedBeforeListenerCutover {
		removedPaths, err := removeStaleAppServiceUnit(stdcontext.Background(), privilegedExecutor, names)
		effects.AddPaths(removedPaths...)
		if len(removedPaths) > 0 {
			effects.AddActions("removed stale app systemd service")
		}
		if err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to remove stale app service", err, effects)
		}
		if _, err := systemd.DaemonReload(stdcontext.Background()); err != nil {
			return appDeployFailureWithEffects(formatter, "systemd daemon-reload failed", err, effects)
		}
		effects.AddActions("systemd daemon-reload")
	}

	if cfg.Mode() == appconfig.ModeListen && !appServicePreparedBeforeListenerCutover {
		if _, err := systemd.Enable(stdcontext.Background(), names.ServiceUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to enable app service", err, effects)
		}
		effects.AddActions("enabled app systemd service")
		if _, err := systemd.Restart(stdcontext.Background(), names.ServiceUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to restart app service", err, effects)
		}
		effects.AddActions("restarted app systemd service")
	}
	if cfg.Nginx.GoAccess.Enabled {
		if _, err := systemd.Enable(stdcontext.Background(), names.GoAccessServiceUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to enable GoAccess service", err, effects)
		}
		effects.AddActions("enabled GoAccess systemd service")
		if _, err := systemd.Restart(stdcontext.Background(), names.GoAccessServiceUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to restart GoAccess service", err, effects)
		}
		effects.AddActions("restarted GoAccess systemd service")
	}
	if _, err := systemd.Enable(stdcontext.Background(), names.RenewTimerUnit); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to enable app certificate renewal timer", err, effects)
	}
	effects.AddActions("enabled app certificate renewal timer")
	if _, err := systemd.Start(stdcontext.Background(), names.RenewTimerUnit); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to start app certificate renewal timer", err, effects)
	}
	effects.AddActions("started app certificate renewal timer")
	if realIPPrepared {
		if _, err := systemd.Enable(stdcontext.Background(), realIPNames.RefreshTimerUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to enable realip refresh timer", err, effects)
		}
		effects.AddActions("enabled realip refresh timer")
		if _, err := systemd.Start(stdcontext.Background(), realIPNames.RefreshTimerUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "Failed to start realip refresh timer", err, effects)
		}
		effects.AddActions("started realip refresh timer")
	}

	if err := releaseRealIPLock(); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to release EdgeOne realip profile lock", err, effects)
	}
	if err := cleanupStaleAppRealIPReferences(stdcontext.Background(), privilegedExecutor, cfg.App.Name, cfg.Nginx.RealIPProfile, &effects); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to clean stale realip references", err, effects)
	}

	fields := append(appConfigFields(options.configPath, cfg),
		output.Field{Label: "nginx site", Value: names.NginxAvailablePath},
		output.Field{Label: "renew timer", Value: names.RenewTimerUnit},
	)
	fields = append(fields, appGoAccessDeployFields(cfg, names)...)
	fields = append(fields, appRealIPDeployFields(cfg, realIPNames, realIPState, realIPPrepared)...)
	fields = append(fields, effects.Fields()...)
	fields = append(fields, appPreflightWarningFields(preflightReport)...)
	nextSteps := []string{
		appDeployRuntimeVerifyStep(options.configPath, cfg),
	}
	nextSteps = append(nextSteps, appGoAccessDeployNextSteps(cfg, names)...)
	nextSteps = append(nextSteps, appPreflightWarningNextSteps(preflightReport)...)
	return formatter.Write(output.Response{
		Command:   "app deploy",
		Status:    "applied",
		Summary:   "App deployed or refreshed from config",
		Fields:    fields,
		NextSteps: nextSteps,
	})
}

func appDeployRootRequiredResponse(permissions preflight.PermissionState) output.Response {
	detail := "fail: app deploy requires root privileges"
	if strings.TrimSpace(permissions.User) != "" {
		detail += ", current user: " + strings.TrimSpace(permissions.User)
	}
	return output.Response{
		Command:   "app deploy",
		Status:    "blocked",
		Summary:   "app deploy preflight found 1 failed check",
		Fields:    []output.Field{{Label: "check permissions", Value: detail}},
		NextSteps: []string{"Rerun with sudo lanpanel app deploy."},
	}
}

type realIPProfileLock interface {
	Release() error
}

type realIPFileLock struct {
	path string
	file *os.File
}

func acquireRealIPProfileLock(profileName string) (realIPProfileLock, error) {
	path, err := realIPProfileLockPath(profileName)
	if err != nil {
		return nil, err
	}
	if err := ensureRealIPLockDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fd, err := openRealIPLockFile(path)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	lock := &realIPFileLock{path: path, file: file}
	if err := validateRealIPLockFile(file, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock realip profile %s with %s: %w", strings.TrimSpace(profileName), path, err)
	}
	return lock, nil
}

func realIPProfileLockPath(profileName string) (string, error) {
	profileName = strings.TrimSpace(profileName)
	if _, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, ""); err != nil {
		return "", fmt.Errorf("realip profile lock name %q is invalid: %w", profileName, err)
	}
	return filepath.Join(realIPLockDir, "lanpanel-realip-"+profileName+".lock"), nil
}

func ensureRealIPLockDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create realip lock directory %s: %w", path, err)
	}
	var stat syscall.Stat_t
	if err := syscall.Lstat(path, &stat); err != nil {
		return fmt.Errorf("stat realip lock directory %s: %w", path, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fmt.Errorf("realip lock directory %s must be a directory", path)
	}
	if stat.Uid != 0 {
		return fmt.Errorf("realip lock directory %s must be owned by root", path)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("realip lock directory %s must be root-only, for example mode 0700", path)
	}
	return nil
}

func openRealIPLockFile(path string) (int, error) {
	flags := syscall.O_RDWR | syscall.O_CREAT | syscall.O_EXCL | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	fd, err := syscall.Open(path, flags, 0o600)
	if err == nil {
		return fd, nil
	}
	if !errors.Is(err, syscall.EEXIST) {
		return -1, fmt.Errorf("open realip lock %s: %w", path, err)
	}
	fd, err = syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == nil {
		return fd, nil
	}
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		fd, err = syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err == nil {
			return fd, nil
		}
	}
	return -1, fmt.Errorf("open realip lock %s: %w", path, err)
}

func validateRealIPLockFile(file *os.File, path string) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("stat realip lock %s: %w", path, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("realip lock %s must be a regular file", path)
	}
	if stat.Uid != 0 {
		return fmt.Errorf("realip lock %s must be owned by root", path)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("realip lock %s must be root-only, for example mode 0600", path)
	}
	return nil
}

func (lock *realIPFileLock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if unlockErr != nil {
		return fmt.Errorf("unlock realip profile lock %s: %w", lock.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close realip profile lock %s: %w", lock.path, closeErr)
	}
	return nil
}

func loadAppConfigForResponse(path string, command string) (appconfig.Config, output.Response, bool) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return appconfig.Config{}, output.Response{
				Command: command,
				Status:  "missing-config",
				Summary: "App config file not found",
				Fields:  []output.Field{{Label: "config path", Value: path}},
				NextSteps: []string{
					fmt.Sprintf("Run 'lanpanel app init --config %s' to generate an example config.", path),
				},
			}, false
		}
		return appconfig.Config{}, output.Response{Command: command, Status: "failed", Summary: "Failed to stat app config file", Fields: []output.Field{{Label: "details", Value: err.Error()}}}, false
	}
	cfg, err := appconfig.LoadFile(path)
	if err != nil {
		return appconfig.Config{}, output.Response{
			Command: command,
			Status:  "invalid-config",
			Summary: "App config file exists but validation failed",
			Fields:  []output.Field{{Label: "config path", Value: path}, {Label: "details", Value: err.Error()}},
			NextSteps: []string{
				fmt.Sprintf("Fix %s and rerun the command.", path),
			},
		}, false
	}
	return cfg, output.Response{}, true
}

func prepareAppRealIPProfile(ctx stdcontext.Context, cfg appconfig.Config, fileSystem host.FileSystem) (appsvc.RealIPProfileNames, realip.State, []realiprender.StagedFile, error) {
	profile, reference, enabled, err := realIPProfileFromAppConfig(cfg)
	if err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	if !enabled {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, nil
	}
	names, err := appsvc.NewRealIPProfileNames(profile.Name, profile.Provider, cfg.App.Name)
	if err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	if err := guardRealIPProfileDirectories(fileSystem, names); err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	if existing, ok, err := readRealIPProfileFromFS(fileSystem, names.MetadataPath); err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	} else if ok {
		if err := ensureRealIPProfileCompatible(existing, profile); err != nil {
			return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
		}
	}
	existingReferences, err := readDeployedRealIPReferencesFn(names.ReferenceDir)
	if err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	profile.Domains = domainsFromRealIPReferences(replaceRealIPReferenceForApp(existingReferences, reference))
	credentials, err := loadEdgeOneCredentialsFn(edgeone.OSFileSystem{}, profile.EnvFile)
	if err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	info, err := describeEdgeOneOriginACLFn(ctx, credentials, profile.ZoneID)
	if err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	state, err := edgeone.BuildState(profile.Name, profile.ZoneID, info, profile.Domains, time.Now())
	if err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	staged, err := stageRealIPRuntimeFilesFn(profile, state, reference)
	if err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	if err := guardRealIPOwnership(fileSystem, profile.Name, profile.Provider, staged); err != nil {
		return appsvc.RealIPProfileNames{}, realip.State{}, nil, err
	}
	return names, state, staged, nil
}

func realIPProfileFromAppConfig(cfg appconfig.Config) (realip.ProfileConfig, realip.Reference, bool, error) {
	profileName := strings.TrimSpace(cfg.Nginx.RealIPProfile)
	if profileName == "" {
		return realip.ProfileConfig{}, realip.Reference{}, false, nil
	}
	profileCfg, ok := cfg.RealIPProfile(profileName)
	if !ok || !profileCfg.IsEnabled() {
		return realip.ProfileConfig{}, realip.Reference{}, false, fmt.Errorf("nginx.realip_profile %q is not an enabled profile", profileName)
	}
	profile := edgeone.ProfileConfig(profileName, profileCfg.EdgeOne.ZoneID, profileCfg.EdgeOne.EnvFile, profileCfg.EffectiveRefreshInterval(), cfg.App.Domains)
	reference := realip.Reference{
		AppName: cfg.App.Name,
		Profile: profileName,
		Domains: append([]string(nil), cfg.App.Domains...),
	}
	return profile, reference, true, nil
}

func guardRealIPOwnership(fileSystem host.FileSystem, profileName string, provider string, staged []realiprender.StagedFile) error {
	marker := realIPManagedMarker(profileName, provider)
	jsonMarker := `"lanpanel_managed": "` + marker + `"`
	for _, file := range staged {
		info, err := fileSystem.Lstat(file.HostPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat existing realip artifact %s: %w", file.HostPath, err)
		}
		if err := validateExistingRealIPArtifactInfo(file.HostPath, info); err != nil {
			return err
		}
		current, err := fileSystem.ReadFile(file.HostPath)
		if err != nil {
			return fmt.Errorf("read existing realip artifact %s: %w", file.HostPath, err)
		}
		text := string(current)
		if strings.Contains(text, marker) || strings.Contains(text, jsonMarker) {
			continue
		}
		return fmt.Errorf("%s exists and is not a Lanpanel-managed realip artifact for profile %s", file.HostPath, profileName)
	}
	return nil
}

func validateExistingRealIPArtifactInfo(path string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("existing realip artifact %s must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("existing realip artifact %s must be a regular file", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("existing realip artifact %s must not be writable by group or others", path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return fmt.Errorf("existing realip artifact %s owner could not be inspected", path)
	}
	if uid != 0 {
		return fmt.Errorf("existing realip artifact %s must be owned by root", path)
	}
	return nil
}

func guardRealIPProfileDirectories(fileSystem host.FileSystem, names appsvc.RealIPProfileNames) error {
	if fileSystem == nil {
		fileSystem = host.OSFileSystem{}
	}
	for _, dir := range []struct {
		label string
		path  string
	}{
		{label: "realip Nginx directory", path: names.NginxDir},
		{label: "realip state directory", path: names.StateDir},
		{label: "realip reference directory", path: names.ReferenceDir},
	} {
		if err := validateExistingRealIPDirectoryChain(fileSystem, dir.label, dir.path); err != nil {
			return err
		}
	}
	exists, err := realIPPathExists(fileSystem, names.StateDir)
	if err != nil {
		return err
	}
	if exists {
		return validateExistingRealIPMetadata(fileSystem, names)
	}
	return nil
}

func validateExistingRealIPDirectoryChain(fileSystem host.FileSystem, label string, path string) error {
	return validateExistingRealIPDirectoryChainWithLstat(label, path, fileSystem.Lstat)
}

func validateExistingRealIPDirectoryChainWithLstat(label string, path string, lstat func(string) (fs.FileInfo, error)) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s path is required and must be absolute", label)
	}
	current := string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(path, string(os.PathSeparator)), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("stat %s %s: %w", label, current, err)
		}
		if err := validateExistingRealIPDirectoryInfo(label, current, info); err != nil {
			return err
		}
	}
	return nil
}

func validateExistingRealIPDirectoryInfo(label string, path string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s %s must not be a symlink", label, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %s must be a directory", label, path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s %s must not be writable by group or others", label, path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return fmt.Errorf("%s %s owner could not be inspected", label, path)
	}
	if uid != 0 {
		return fmt.Errorf("%s %s must be owned by root", label, path)
	}
	return nil
}

func realIPPathExists(fileSystem host.FileSystem, path string) (bool, error) {
	_, err := fileSystem.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat realip path %s: %w", path, err)
	}
	return true, nil
}

func validateExistingRealIPMetadata(fileSystem host.FileSystem, names appsvc.RealIPProfileNames) error {
	info, err := fileSystem.Lstat(names.MetadataPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s exists without %s; refusing to write into a non-Lanpanel realip profile root", names.StateDir, names.MetadataPath)
		}
		return fmt.Errorf("stat realip profile metadata %s: %w", names.MetadataPath, err)
	}
	return validateExistingRealIPMetadataInfo(names.MetadataPath, info)
}

func validateExistingRealIPMetadataInfo(path string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to trust realip profile ownership", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; refusing to trust realip profile ownership", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be root-only, for example mode 0600", path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return fmt.Errorf("%s owner could not be inspected", path)
	}
	if uid != 0 {
		return fmt.Errorf("%s must be owned by root", path)
	}
	return nil
}

func realIPManagedMarker(profileName string, provider string) string {
	return "Lanpanel-managed: realip.profile=" + strings.TrimSpace(profileName) + " provider=" + strings.TrimSpace(provider)
}

type appNginxSiteSnapshot struct {
	availablePath    string
	availableExists  bool
	availableContent []byte
	availableMode    fs.FileMode
	enabledPath      string
	enabledExists    bool
}

func snapshotAppNginxSite(fileSystem host.FileSystem, names appsvc.Names) (appNginxSiteSnapshot, error) {
	snapshot := appNginxSiteSnapshot{
		availablePath: strings.TrimSpace(names.NginxAvailablePath),
		enabledPath:   strings.TrimSpace(names.NginxEnabledPath),
	}
	if snapshot.availablePath == "" || snapshot.enabledPath == "" {
		return appNginxSiteSnapshot{}, fmt.Errorf("app Nginx site paths are required")
	}
	info, err := fileSystem.Lstat(snapshot.availablePath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
			return appNginxSiteSnapshot{}, fmt.Errorf("stat existing app Nginx site %s: %w", snapshot.availablePath, err)
		}
	} else {
		if info.Mode()&fs.ModeSymlink != 0 {
			return appNginxSiteSnapshot{}, fmt.Errorf("%s is a symlink; refusing to snapshot app Nginx site", snapshot.availablePath)
		}
		if !info.Mode().IsRegular() {
			return appNginxSiteSnapshot{}, fmt.Errorf("%s exists and is not a regular app Nginx site file", snapshot.availablePath)
		}
		content, err := fileSystem.ReadFile(snapshot.availablePath)
		if err != nil {
			return appNginxSiteSnapshot{}, fmt.Errorf("read existing app Nginx site %s: %w", snapshot.availablePath, err)
		}
		snapshot.availableExists = true
		snapshot.availableContent = append([]byte(nil), content...)
		snapshot.availableMode = info.Mode().Perm()
	}
	enabledInfo, err := fileSystem.Lstat(snapshot.enabledPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
			return appNginxSiteSnapshot{}, fmt.Errorf("stat existing enabled app Nginx site %s: %w", snapshot.enabledPath, err)
		}
		return snapshot, nil
	}
	if enabledInfo.Mode()&fs.ModeSymlink == 0 {
		return appNginxSiteSnapshot{}, fmt.Errorf("%s exists and is not an enabled app Nginx symlink", snapshot.enabledPath)
	}
	snapshot.enabledExists = true
	return snapshot, nil
}

func restoreAppNginxSite(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, snapshot appNginxSiteSnapshot) error {
	if strings.TrimSpace(snapshot.availablePath) == "" || strings.TrimSpace(snapshot.enabledPath) == "" {
		return fmt.Errorf("app Nginx site snapshot paths are required")
	}
	if snapshot.availableExists {
		if err := fileSystem.WriteFile(snapshot.availablePath, snapshot.availableContent, snapshot.availableMode); err != nil {
			return fmt.Errorf("restore app Nginx site %s: %w", snapshot.availablePath, err)
		}
	} else if err := removePath(ctx, executor, "rollback-app-nginx-site", snapshot.availablePath); err != nil {
		return err
	}
	if !snapshot.enabledExists {
		if err := removePath(ctx, executor, "rollback-app-nginx-enabled-site", snapshot.enabledPath); err != nil {
			return err
		}
	}
	return nil
}

type appRealIPReferenceSnapshot struct {
	hostPath string
	exists   bool
	content  []byte
	mode     fs.FileMode
}

func snapshotAppRealIPReference(fileSystem host.FileSystem, path string) (appRealIPReferenceSnapshot, error) {
	snapshot := appRealIPReferenceSnapshot{hostPath: strings.TrimSpace(path)}
	if snapshot.hostPath == "" {
		return appRealIPReferenceSnapshot{}, fmt.Errorf("app realip reference path is required")
	}
	info, err := fileSystem.Lstat(snapshot.hostPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return snapshot, nil
		}
		return appRealIPReferenceSnapshot{}, fmt.Errorf("stat existing app realip reference %s: %w", snapshot.hostPath, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return appRealIPReferenceSnapshot{}, fmt.Errorf("%s is a symlink; refusing to snapshot app realip reference", snapshot.hostPath)
	}
	if !info.Mode().IsRegular() {
		return appRealIPReferenceSnapshot{}, fmt.Errorf("%s exists and is not a regular app realip reference file", snapshot.hostPath)
	}
	content, err := fileSystem.ReadFile(snapshot.hostPath)
	if err != nil {
		return appRealIPReferenceSnapshot{}, fmt.Errorf("read existing app realip reference %s: %w", snapshot.hostPath, err)
	}
	snapshot.exists = true
	snapshot.content = append([]byte(nil), content...)
	snapshot.mode = info.Mode().Perm()
	return snapshot, nil
}

func restoreAppRealIPReference(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, snapshot appRealIPReferenceSnapshot) error {
	if strings.TrimSpace(snapshot.hostPath) == "" {
		return fmt.Errorf("app realip reference snapshot path is required")
	}
	if snapshot.exists {
		return fileSystem.WriteFile(snapshot.hostPath, snapshot.content, snapshot.mode)
	}
	return removePath(ctx, executor, "rollback-realip-reference", snapshot.hostPath)
}

func removePath(ctx stdcontext.Context, executor host.Executor, displayName string, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("path is required for %s", displayName)
	}
	_, err := executor.Run(ctx, host.Command{
		Name:        "rm",
		Args:        []string{"-f", "--", path},
		DisplayName: displayName,
		DisplayArgs: []string{path},
	})
	return err
}

func removeEmptyDir(ctx stdcontext.Context, executor host.Executor, displayName string, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("path is required for %s", displayName)
	}
	_, err := executor.Run(ctx, host.Command{
		Name:        "rmdir",
		Args:        []string{"--", path},
		DisplayName: displayName,
		DisplayArgs: []string{path},
	})
	return err
}

func readRealIPProfileFromFS(fileSystem host.FileSystem, path string) (realip.ProfileConfig, bool, error) {
	data, err := fileSystem.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return realip.ProfileConfig{}, false, nil
		}
		return realip.ProfileConfig{}, false, fmt.Errorf("read existing realip profile metadata %s: %w", path, err)
	}
	profileName, err := realIPProfileNameFromMetadataPath(path)
	if err != nil {
		return realip.ProfileConfig{}, false, fmt.Errorf("read existing realip profile metadata %s: %w", path, err)
	}
	profile, err := parseDeployedRealIPProfile(data, path, profileName)
	if err != nil {
		return realip.ProfileConfig{}, false, err
	}
	return profile, true, nil
}

func ensureRealIPProfileCompatible(existing realip.ProfileConfig, desired realip.ProfileConfig) error {
	mismatches := []string{}
	if existing.Name != desired.Name {
		mismatches = append(mismatches, "name")
	}
	if existing.Provider != desired.Provider {
		mismatches = append(mismatches, "provider")
	}
	if existing.ZoneID != desired.ZoneID {
		mismatches = append(mismatches, "edgeone.zone_id")
	}
	if existing.EnvFile != desired.EnvFile {
		mismatches = append(mismatches, "edgeone.env_file")
	}
	if existing.RefreshInterval != desired.RefreshInterval {
		mismatches = append(mismatches, "refresh_interval")
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("existing realip profile %s differs in %s; refusing to reuse shared profile with changed contract", existing.Name, strings.Join(mismatches, ", "))
	}
	return nil
}

func cleanupStaleAppRealIPReferences(ctx stdcontext.Context, executor host.Executor, appName string, currentProfile string, effects *appDeployEffects) error {
	appName = strings.TrimSpace(appName)
	currentProfile = strings.TrimSpace(currentProfile)
	if appName == "" {
		return nil
	}
	if _, err := appsvc.NewRealIPProfileNames("edgeone-prod", appconfig.RealIPProviderEdgeOne, appName); err != nil {
		return fmt.Errorf("cleanup stale realip references for app %q: %w", appName, err)
	}
	staleProfiles, err := staleAppRealIPReferenceProfiles(appName, currentProfile)
	if err != nil {
		return err
	}
	if len(staleProfiles) == 0 {
		effects.AddActions("checked stale realip references")
		return nil
	}
	validatorPath, err := currentExecutablePathFn()
	if err != nil {
		return fmt.Errorf("resolve lanpanel executable for realip reference validation: %w", err)
	}
	validatorPath = strings.TrimSpace(validatorPath)
	if validatorPath == "" {
		return fmt.Errorf("resolve lanpanel executable for realip reference validation: empty path")
	}
	changed := false
	for _, profile := range staleProfiles {
		lock, err := acquireRealIPProfileLockFn(profile)
		if err != nil {
			return err
		}
		cleaned := false
		cleanupErr := validateLockedStaleAppRealIPCleanupTargets(appName, profile)
		if cleanupErr == nil {
			cleaned, cleanupErr = cleanupStaleAppRealIPReferenceForProfile(ctx, executor, appName, profile, validatorPath)
		}
		releaseErr := lock.Release()
		if cleanupErr != nil {
			if releaseErr != nil {
				return fmt.Errorf("%w; release realip profile lock failed: %v", cleanupErr, releaseErr)
			}
			return cleanupErr
		}
		if releaseErr != nil {
			return releaseErr
		}
		changed = changed || cleaned
	}
	if changed {
		if _, err := executor.Systemctl(ctx, "daemon-reload"); err != nil {
			return err
		}
		effects.AddActions("cleaned stale realip references")
	} else {
		effects.AddActions("checked stale realip references")
	}
	return nil
}

func staleAppRealIPReferenceProfiles(appName string, currentProfile string) ([]string, error) {
	pattern := filepath.Join(realIPCleanupRoot, "*", "references", appName+".json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("scan stale realip references for app %s: %w", appName, err)
	}
	profiles := []string{}
	seen := map[string]struct{}{}
	for _, ref := range matches {
		profile := filepath.Base(filepath.Dir(filepath.Dir(ref)))
		if profile == currentProfile {
			continue
		}
		if _, err := appsvc.NewRealIPProfileNames(profile, appconfig.RealIPProviderEdgeOne, appName); err != nil {
			return nil, fmt.Errorf("%s profile path is not safe for Lanpanel-managed realip cleanup: %w", ref, err)
		}
		if err := validateStaleAppRealIPReferencePath(appName, profile, ref); err != nil {
			return nil, err
		}
		if _, ok := seen[profile]; ok {
			continue
		}
		seen[profile] = struct{}{}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func validateLockedStaleAppRealIPCleanupTargets(appName string, profile string) error {
	ref := filepath.Join(realIPCleanupRoot, profile, "references", appName+".json")
	if _, err := lstatRealIPCleanupPathFn(ref); err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat stale realip reference %s: %w", ref, err)
	}
	if err := validateStaleAppRealIPReferencePath(appName, profile, ref); err != nil {
		return err
	}
	hasOtherReferences, err := staleRealIPProfileHasOtherJSONReferences(appName, profile)
	if err != nil {
		return err
	}
	if hasOtherReferences {
		return nil
	}
	return validateStaleRealIPSharedCleanupTargets(profile)
}

func staleRealIPProfileHasOtherJSONReferences(appName string, profile string) (bool, error) {
	refDir := filepath.Join(realIPCleanupRoot, profile, "references")
	currentRef := filepath.Clean(filepath.Join(refDir, appName+".json"))
	matches, err := filepath.Glob(filepath.Join(refDir, "*.json"))
	if err != nil {
		return false, fmt.Errorf("scan stale realip references for profile %s: %w", profile, err)
	}
	for _, match := range matches {
		if filepath.Clean(match) != currentRef {
			return true, nil
		}
	}
	return false, nil
}

func validateStaleRealIPSharedCleanupTargets(profile string) error {
	names, err := appsvc.NewRealIPProfileNames(profile, appconfig.RealIPProviderEdgeOne, "")
	if err != nil {
		return fmt.Errorf("stale realip cleanup profile %q is invalid: %w", profile, err)
	}
	for _, dir := range []struct {
		label string
		path  string
	}{
		{label: "stale realip systemd directory", path: filepath.Dir(names.RefreshServicePath)},
		{label: "stale realip Nginx directory", path: names.NginxDir},
	} {
		if err := validateExistingRealIPDirectoryChainWithLstat(dir.label, dir.path, lstatRealIPCleanupPathFn); err != nil {
			return err
		}
	}
	marker := realIPManagedMarker(profile, appconfig.RealIPProviderEdgeOne)
	profileDir := filepath.Join(realIPCleanupRoot, profile)
	for _, artifact := range []struct {
		label    string
		path     string
		validate func(string, fs.FileInfo) error
	}{
		{label: "stale realip refresh service", path: names.RefreshServicePath, validate: validateExistingRealIPArtifactInfo},
		{label: "stale realip refresh timer", path: names.RefreshTimerPath, validate: validateExistingRealIPArtifactInfo},
		{label: "stale realip Nginx include", path: names.NginxIncludePath, validate: validateExistingRealIPArtifactInfo},
		{label: "stale realip trusted CIDR include", path: names.TrustedCIDRPath, validate: validateExistingRealIPArtifactInfo},
		{label: "stale realip state", path: filepath.Join(profileDir, "state.json"), validate: validateDeployedRealIPStateInfo},
		{label: "stale realip metadata", path: filepath.Join(profileDir, "profile.json"), validate: validateExistingRealIPMetadataInfo},
	} {
		if err := validateOptionalStaleRealIPCleanupArtifact(artifact.label, artifact.path, marker, artifact.validate); err != nil {
			return err
		}
	}
	return nil
}

func validateOptionalStaleRealIPCleanupArtifact(label string, path string, marker string, validate func(string, fs.FileInfo) error) error {
	info, err := lstatRealIPCleanupPathFn(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %s %s: %w", label, path, err)
	}
	if err := validate(path, info); err != nil {
		return err
	}
	content, err := readDeployedRealIPArtifactFn(path)
	if err != nil {
		return fmt.Errorf("read %s %s: %w", label, path, err)
	}
	if !strings.Contains(string(content), marker) {
		return fmt.Errorf("%s %s missing marker %q", label, path, marker)
	}
	return nil
}

func validateStaleAppRealIPReferencePath(appName string, profile string, ref string) error {
	root := filepath.Clean(strings.TrimSpace(realIPCleanupRoot))
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("realip cleanup root %q is required and must be absolute", realIPCleanupRoot)
	}
	expected := filepath.Join(root, profile, "references", appName+".json")
	ref = filepath.Clean(ref)
	if ref != expected {
		return fmt.Errorf("%s is not the expected Lanpanel-managed stale realip reference path %s", ref, expected)
	}
	relativeRef, err := filepath.Rel(root, ref)
	if err != nil {
		return fmt.Errorf("verify stale realip reference %s is under cleanup root %s: %w", ref, root, err)
	}
	if relativeRef == "." || strings.HasPrefix(relativeRef, ".."+string(os.PathSeparator)) || relativeRef == ".." || filepath.IsAbs(relativeRef) {
		return fmt.Errorf("stale realip reference %s must remain under cleanup root %s", ref, root)
	}
	for _, dir := range []struct {
		label string
		path  string
	}{
		{label: "realip cleanup root", path: root},
		{label: "stale realip profile directory", path: filepath.Join(root, profile)},
		{label: "stale realip reference directory", path: filepath.Join(root, profile, "references")},
	} {
		info, err := lstatRealIPCleanupPathFn(dir.path)
		if err != nil {
			return fmt.Errorf("stat %s %s: %w", dir.label, dir.path, err)
		}
		if err := validateExistingRealIPDirectoryInfo(dir.label, dir.path, info); err != nil {
			return err
		}
	}
	info, err := lstatRealIPCleanupPathFn(ref)
	if err != nil {
		return fmt.Errorf("stat stale realip reference %s: %w", ref, err)
	}
	return validateDeployedRealIPReferenceInfo(ref, info)
}

func cleanupStaleAppRealIPReferenceForProfile(ctx stdcontext.Context, executor host.Executor, appName string, profile string, validatorPath string) (bool, error) {
	script := `set -eu
app=$1
profile=$2
validator=$3
root=$4
nginx_binary=$5
ref="$root/$profile/references/$app.json"
[ -e "$ref" ] || exit 0
marker="Lanpanel-managed: realip.profile=$profile provider=edgeone"
refdir=$(dirname "$ref")
profiledir=$(dirname "$refdir")
other_references=0
for other_ref in "$refdir"/*.json; do
    [ -e "$other_ref" ] || [ -L "$other_ref" ] || continue
    other_app=$(basename "$other_ref" .json)
    if ! "$validator" app realip validate-reference --profile "$profile" --app "$other_app" --path "$other_ref" >/dev/null; then
        echo "$other_ref is not a valid Lanpanel-managed realip reference for profile $profile; refusing stale cleanup" >&2
        exit 1
    fi
    if [ "$other_ref" != "$ref" ]; then
        other_references=$((other_references + 1))
    fi
done
has_other_references=false
if [ "$other_references" -gt 0 ]; then
    has_other_references=true
fi
if [ "$has_other_references" = false ]; then
    service="/etc/systemd/system/lanpanel-realip-$profile-refresh.service"
    timer="/etc/systemd/system/lanpanel-realip-$profile-refresh.timer"
    nginx_dir="/etc/nginx/lanpanel/realip/$profile"
    nginx_active="/etc/nginx/lanpanel/realip/$profile/active.conf"
    nginx_trusted="/etc/nginx/lanpanel/realip/$profile/trusted-cidrs.conf"
    state="$profiledir/state.json"
    metadata="$profiledir/profile.json"
    for artifact in "$service" "$timer" "$nginx_active" "$nginx_trusted" "$state" "$metadata"; do
        [ -e "$artifact" ] || [ -L "$artifact" ] || continue
        if [ -L "$artifact" ] || [ ! -f "$artifact" ]; then
            echo "$artifact exists but is not a regular Lanpanel-managed realip artifact; refusing stale cleanup" >&2
            exit 1
        fi
        if ! grep -Fq "$marker" "$artifact"; then
            echo "$artifact exists but is not this profile's Lanpanel-managed realip artifact; refusing stale cleanup" >&2
            exit 1
        fi
    done
    dump=$(mktemp)
    trap 'rm -f "$dump"' EXIT INT TERM
    if ! "$nginx_binary" -T > "$dump" 2>&1; then
        cat "$dump" >&2
        exit 1
    fi
    if grep -Fq "$marker" "$dump"; then
        echo "$marker is still present in active Nginx config; refusing stale cleanup" >&2
        exit 1
    fi
    for include in "$nginx_active" "$nginx_trusted"; do
        if grep -Fq "# configuration file $include:" "$dump"; then
            echo "$include is still referenced by active Nginx config; refusing stale cleanup" >&2
            exit 1
        fi
    done
    for entry in "$refdir"/* "$refdir"/.[!.]* "$refdir"/..?*; do
        [ -e "$entry" ] || [ -L "$entry" ] || continue
        case "$entry" in
            "$ref")
                ;;
            *)
                echo "$refdir contains unexpected stale realip reference entry $entry; refusing stale cleanup" >&2
                exit 1
                ;;
        esac
    done
    for entry in "$profiledir"/* "$profiledir"/.[!.]* "$profiledir"/..?*; do
        [ -e "$entry" ] || [ -L "$entry" ] || continue
        case "$entry" in
            "$refdir"|"$state"|"$metadata")
                ;;
            *)
                echo "$profiledir contains unexpected stale realip profile entry $entry; refusing stale cleanup" >&2
                exit 1
                ;;
        esac
    done
    for entry in "$nginx_dir"/* "$nginx_dir"/.[!.]* "$nginx_dir"/..?*; do
        [ -e "$entry" ] || [ -L "$entry" ] || continue
        case "$entry" in
            "$nginx_active"|"$nginx_trusted")
                ;;
            *)
                echo "$nginx_dir contains unexpected stale realip nginx entry $entry; refusing stale cleanup" >&2
                exit 1
                ;;
        esac
    done
    if [ -e "$timer" ]; then
        systemctl disable --now "lanpanel-realip-$profile-refresh.timer"
    fi
    if [ -e "$service" ]; then
        systemctl stop "lanpanel-realip-$profile-refresh.service"
    fi
    rm -f -- "$ref"
    rm -f -- "/etc/systemd/system/lanpanel-realip-$profile-refresh.service" \
              "/etc/systemd/system/lanpanel-realip-$profile-refresh.timer" \
              "/etc/nginx/lanpanel/realip/$profile/active.conf" \
              "/etc/nginx/lanpanel/realip/$profile/trusted-cidrs.conf" \
              "$profiledir/state.json" "$profiledir/profile.json"
    for stale_dir in "$refdir" "$profiledir" "$nginx_dir"; do
        [ -d "$stale_dir" ] || continue
        if ! rmdir "$stale_dir"; then
            echo "failed to remove stale realip directory $stale_dir" >&2
            exit 1
        fi
    done
else
    rm -f -- "$ref"
fi
echo lanpanel-realip-cleaned`
	result, err := executor.Run(ctx, host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "lanpanel-app-realip-stale-reference-cleanup", appName, profile, validatorPath, realIPCleanupRoot, appsvc.NginxBinaryPath},
		DisplayName: "cleanup-realip-references",
		DisplayArgs: []string{appName, profile},
	})
	if err != nil {
		return false, err
	}
	return strings.Contains(result.Stdout, "lanpanel-realip-cleaned"), nil
}

func refreshDeployedRealIPProfile(ctx stdcontext.Context, profileName string, effects *appDeployEffects) (state realip.State, profile realip.ProfileConfig, names appsvc.RealIPProfileNames, returnErr error) {
	names, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, "")
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	lock, err := acquireRealIPProfileLockFn(profileName)
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			if returnErr != nil {
				returnErr = fmt.Errorf("%w; release EdgeOne realip profile lock failed: %v", returnErr, err)
				return
			}
			returnErr = err
		}
	}()
	permissions := detectPermissionStateFn()
	privilege := deployPrivilegeStrategy(permissions)
	executor := newHostExecutorFn(nil).WithPrivilege(privilege)
	fileSystem := newAppHostFileSystemFn(executor, privilege)
	if err := guardRealIPProfileDirectories(fileSystem, names); err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	profile, err = readDeployedRealIPProfileFn(names.MetadataPath)
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	if profile.Name != profileName {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, fmt.Errorf("deployed profile metadata name %q does not match requested profile %q", profile.Name, profileName)
	}
	if profile.Provider != appconfig.RealIPProviderEdgeOne {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, fmt.Errorf("deployed realip provider %q is not supported", profile.Provider)
	}
	references, err := readDeployedRealIPReferencesFn(names.ReferenceDir)
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	domains := domainsFromRealIPReferences(references)
	if len(domains) == 0 {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, fmt.Errorf("no deployed app references found for realip profile %s", profileName)
	}
	profile.Domains = domains
	credentials, err := loadEdgeOneCredentialsFn(edgeone.OSFileSystem{}, profile.EnvFile)
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	info, err := describeEdgeOneOriginACLFn(ctx, credentials, profile.ZoneID)
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	state, err = edgeone.BuildState(profile.Name, profile.ZoneID, info, domains, time.Now())
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	staged, err := stageRealIPRuntimeFilesFn(profile, state, realip.Reference{})
	if err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}

	if err := guardRealIPOwnership(fileSystem, profile.Name, profile.Provider, staged); err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	if err := installAndActivateRealIPRefresh(ctx, executor, fileSystem, newAppFileInstallerFn(executor, privilege), staged, effects); err != nil {
		return realip.State{}, realip.ProfileConfig{}, appsvc.RealIPProfileNames{}, err
	}
	return state, profile, names, nil
}

type realIPFileSnapshot struct {
	hostPath string
	exists   bool
	content  []byte
	mode     fs.FileMode
}

type realIPDirectorySnapshot struct {
	path   string
	exists bool
}

type realIPDirectorySpec struct {
	label string
	path  string
}

func installAndActivateRealIPRefresh(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, installer appStagedFileInstaller, staged []realiprender.StagedFile, effects *appDeployEffects) error {
	snapshots, err := snapshotRealIPFiles(fileSystem, staged, true)
	if err != nil {
		return err
	}

	results, err := installer.Install(realiprender.ConvertStagedFiles(staged))
	effects.AddPaths(host.CollectModifiedPaths(results)...)
	if err != nil {
		if rollbackErr := restoreRealIPFileSnapshots(ctx, executor, fileSystem, snapshots); rollbackErr != nil {
			return fmt.Errorf("install realip artifacts failed: %w; rollback also failed: %v", err, rollbackErr)
		}
		return err
	}
	effects.AddActions("installed realip artifacts")
	systemd := newHostSystemdFn(executor)
	if _, err := systemd.DaemonReload(ctx); err != nil {
		return rollbackRealIPRefresh(ctx, executor, fileSystem, snapshots, fmt.Errorf("systemd daemon-reload failed after realip refresh: %w", err), false)
	}
	effects.AddActions("systemd daemon-reload")
	if _, err := executor.Run(ctx, appsvc.TestNginxCommand()); err != nil {
		return rollbackRealIPRefresh(ctx, executor, fileSystem, snapshots, fmt.Errorf("nginx -t failed after realip refresh: %w", err), false)
	}
	effects.AddActions("nginx -t")
	if _, err := executor.Run(ctx, appsvc.ReloadNginxCommand()); err != nil {
		return rollbackRealIPRefresh(ctx, executor, fileSystem, snapshots, fmt.Errorf("nginx reload failed after realip refresh: %w", err), true)
	}
	effects.AddActions("reloaded nginx")
	return nil
}

func snapshotRealIPFiles(fileSystem host.FileSystem, staged []realiprender.StagedFile, requireExisting bool) ([]realIPFileSnapshot, error) {
	snapshots := make([]realIPFileSnapshot, 0, len(staged))
	for _, file := range staged {
		hostPath := strings.TrimSpace(file.HostPath)
		if hostPath == "" {
			return nil, fmt.Errorf("realip artifact host path is required")
		}
		info, err := fileSystem.Lstat(hostPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
				if requireExisting {
					return nil, fmt.Errorf("active realip artifact %s missing before refresh: %w", hostPath, err)
				}
				snapshots = append(snapshots, realIPFileSnapshot{hostPath: hostPath})
				continue
			}
			return nil, fmt.Errorf("stat active realip artifact %s: %w", hostPath, err)
		}
		if err := validateExistingRealIPArtifactInfo(hostPath, info); err != nil {
			return nil, err
		}
		content, err := fileSystem.ReadFile(hostPath)
		if err != nil {
			return nil, fmt.Errorf("read active realip artifact %s before refresh: %w", hostPath, err)
		}
		snapshots = append(snapshots, realIPFileSnapshot{
			hostPath: hostPath,
			exists:   true,
			content:  append([]byte(nil), content...),
			mode:     info.Mode().Perm(),
		})
	}
	return snapshots, nil
}

func snapshotRealIPDirectories(fileSystem host.FileSystem, names appsvc.RealIPProfileNames) ([]realIPDirectorySnapshot, error) {
	if fileSystem == nil {
		fileSystem = host.OSFileSystem{}
	}
	dirs := realIPDeployDirectorySpecs(names)
	snapshots := make([]realIPDirectorySnapshot, 0, len(dirs))
	for _, dir := range dirs {
		path := filepath.Clean(strings.TrimSpace(dir.path))
		if path == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("%s path is required and must be absolute", dir.label)
		}
		info, err := fileSystem.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
				snapshots = append(snapshots, realIPDirectorySnapshot{path: path})
				continue
			}
			return nil, fmt.Errorf("stat %s %s before realip deploy: %w", dir.label, path, err)
		}
		if err := validateExistingRealIPDirectoryInfo(dir.label, path, info); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, realIPDirectorySnapshot{path: path, exists: true})
	}
	return snapshots, nil
}

func realIPDeployDirectorySpecs(names appsvc.RealIPProfileNames) []realIPDirectorySpec {
	specs := []realIPDirectorySpec{}
	add := func(label string, path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" {
			specs = append(specs, realIPDirectorySpec{label: label, path: path})
			return
		}
		for _, existing := range specs {
			if existing.path == path {
				return
			}
		}
		specs = append(specs, realIPDirectorySpec{label: label, path: path})
	}
	add("realip reference directory", names.ReferenceDir)
	add("realip state directory", names.StateDir)
	add("realip state root directory", filepath.Dir(names.StateDir))
	add("realip state base directory", filepath.Dir(filepath.Dir(names.StateDir)))
	add("realip Nginx directory", names.NginxDir)
	add("realip Nginx root directory", filepath.Dir(names.NginxDir))
	add("realip Nginx base directory", filepath.Dir(filepath.Dir(names.NginxDir)))
	return specs
}

func rollbackRealIPDeploy(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, snapshots []realIPFileSnapshot, directories []realIPDirectorySnapshot, cause error, reloadRestoredNginx bool) error {
	if rollbackErr := restoreRealIPDeploySnapshots(ctx, executor, fileSystem, snapshots, directories); rollbackErr != nil {
		return fmt.Errorf("%w; rollback EdgeOne realip profile artifacts failed: %v", cause, rollbackErr)
	}
	if err := activateRestoredRealIPRuntime(ctx, executor, cause, reloadRestoredNginx); err != nil {
		return err
	}
	return cause
}

func rollbackRealIPRefresh(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, snapshots []realIPFileSnapshot, cause error, reloadRestoredNginx bool) error {
	if rollbackErr := restoreRealIPFileSnapshots(ctx, executor, fileSystem, snapshots); rollbackErr != nil {
		return fmt.Errorf("%w; rollback also failed: %v", cause, rollbackErr)
	}
	if err := activateRestoredRealIPRuntime(ctx, executor, cause, reloadRestoredNginx); err != nil {
		return err
	}
	return cause
}

func activateRestoredRealIPRuntime(ctx stdcontext.Context, executor host.Executor, cause error, reloadRestoredNginx bool) error {
	systemd := newHostSystemdFn(executor)
	if _, err := systemd.DaemonReload(ctx); err != nil {
		return fmt.Errorf("%w; restored previous realip artifacts but systemd daemon-reload failed: %v", cause, err)
	}
	if _, err := executor.Run(ctx, appsvc.TestNginxCommand()); err != nil {
		return fmt.Errorf("%w; restored previous realip artifacts but nginx -t still failed: %v", cause, err)
	}
	if reloadRestoredNginx {
		if _, err := executor.Run(ctx, appsvc.ReloadNginxCommand()); err != nil {
			return fmt.Errorf("%w; restored previous realip artifacts and nginx -t passed but nginx reload failed: %v", cause, err)
		}
	}
	return nil
}

func restoreRealIPDeploySnapshots(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, snapshots []realIPFileSnapshot, directories []realIPDirectorySnapshot) error {
	if err := restoreRealIPFileSnapshots(ctx, executor, fileSystem, snapshots); err != nil {
		return err
	}
	if err := restoreRealIPDirectorySnapshots(ctx, executor, fileSystem, directories); err != nil {
		return err
	}
	return nil
}

func restoreRealIPFileSnapshots(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, snapshots []realIPFileSnapshot) error {
	for i := len(snapshots) - 1; i >= 0; i-- {
		snapshot := snapshots[i]
		if !snapshot.exists {
			if err := removePath(ctx, executor, "rollback-realip-artifact", snapshot.hostPath); err != nil {
				return fmt.Errorf("remove new realip artifact %s: %w", snapshot.hostPath, err)
			}
			continue
		}
		if err := fileSystem.WriteFile(snapshot.hostPath, snapshot.content, snapshot.mode); err != nil {
			return fmt.Errorf("restore %s: %w", snapshot.hostPath, err)
		}
	}
	return nil
}

func restoreRealIPDirectorySnapshots(ctx stdcontext.Context, executor host.Executor, fileSystem host.FileSystem, snapshots []realIPDirectorySnapshot) error {
	if fileSystem == nil {
		fileSystem = host.OSFileSystem{}
	}
	for _, snapshot := range snapshots {
		if snapshot.exists {
			continue
		}
		path := filepath.Clean(strings.TrimSpace(snapshot.path))
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("realip directory snapshot path is required and must be absolute")
		}
		info, err := fileSystem.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat new realip directory %s before rollback removal: %w", path, err)
		}
		if err := validateExistingRealIPDirectoryInfo("new realip directory", path, info); err != nil {
			return err
		}
		if err := removeEmptyDir(ctx, executor, "rollback-realip-directory", path); err != nil {
			return fmt.Errorf("remove new realip directory %s: %w", path, err)
		}
	}
	return nil
}

func readDeployedRealIPProfile(path string) (realip.ProfileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return realip.ProfileConfig{}, fmt.Errorf("read deployed realip profile metadata %s: %w", path, err)
	}
	profileName, err := realIPProfileNameFromMetadataPath(path)
	if err != nil {
		return realip.ProfileConfig{}, fmt.Errorf("read deployed realip profile metadata %s: %w", path, err)
	}
	profile, err := parseDeployedRealIPProfile(data, path, profileName)
	if err != nil {
		return realip.ProfileConfig{}, err
	}
	return profile, nil
}

func realIPProfileNameFromMetadataPath(path string) (string, error) {
	if filepath.Base(path) != "profile.json" {
		return "", fmt.Errorf("realip profile metadata path must end with profile.json")
	}
	profileName := strings.TrimSpace(filepath.Base(filepath.Dir(path)))
	if _, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, ""); err != nil {
		return "", fmt.Errorf("profile path name is not safe: %w", err)
	}
	return profileName, nil
}

func parseDeployedRealIPProfile(data []byte, path string, profileName string) (realip.ProfileConfig, error) {
	var managed struct {
		LanpanelManaged string `json:"lanpanel_managed"`
		realip.ProfileConfig
	}
	if err := json.Unmarshal(data, &managed); err != nil {
		return realip.ProfileConfig{}, fmt.Errorf("parse deployed realip profile metadata %s: %w", path, err)
	}
	expectedMarker := realIPManagedMarker(profileName, appconfig.RealIPProviderEdgeOne)
	if managed.LanpanelManaged != expectedMarker {
		return realip.ProfileConfig{}, fmt.Errorf("deployed realip profile metadata %s has lanpanel_managed marker %q, want %q", path, managed.LanpanelManaged, expectedMarker)
	}
	profile := managed.ProfileConfig
	if profile.Name != profileName {
		return realip.ProfileConfig{}, fmt.Errorf("deployed realip profile metadata %s name %q does not match %q", path, profile.Name, profileName)
	}
	if profile.Provider != appconfig.RealIPProviderEdgeOne {
		return realip.ProfileConfig{}, fmt.Errorf("deployed realip profile metadata %s provider %q is not supported", path, profile.Provider)
	}
	if !isSafeDeployedEdgeOneZoneID(profile.ZoneID) {
		return realip.ProfileConfig{}, fmt.Errorf("deployed realip profile metadata %s zone_id must be an EdgeOne zone id such as zone-xxxxxxxx", path)
	}
	if err := validateDeployedRealIPEnvFile(path, profile.EnvFile); err != nil {
		return realip.ProfileConfig{}, err
	}
	duration, err := realip.RefreshIntervalDuration(profile.RefreshInterval)
	if err != nil {
		return realip.ProfileConfig{}, fmt.Errorf("deployed realip profile metadata %s %s", path, err.Error())
	}
	if duration < time.Hour {
		return realip.ProfileConfig{}, fmt.Errorf("deployed realip profile metadata %s refresh_interval must be at least 1h; use an explicit refresh command for immediate synchronization", path)
	}
	return profile, nil
}

func ensureDeployedRealIPStateMatchesProfile(profile realip.ProfileConfig, state realip.State) error {
	mismatches := []string{}
	if state.ProfileName != profile.Name {
		mismatches = append(mismatches, fmt.Sprintf("state profile_name %q does not match profile name %q", state.ProfileName, profile.Name))
	}
	if state.Provider != profile.Provider {
		mismatches = append(mismatches, fmt.Sprintf("state provider %q does not match profile provider %q", state.Provider, profile.Provider))
	}
	if state.ZoneID != profile.ZoneID {
		mismatches = append(mismatches, fmt.Sprintf("state zone_id %q does not match profile zone_id %q", state.ZoneID, profile.ZoneID))
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("deployed realip state does not match deployed profile metadata: %s", strings.Join(mismatches, "; "))
	}
	return nil
}

func readDeployedRealIPState(path string, profileName string) (realip.State, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return realip.State{}, fmt.Errorf("stat deployed realip state %s: %w", path, err)
	}
	if err := validateDeployedRealIPStateInfo(path, info); err != nil {
		return realip.State{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return realip.State{}, fmt.Errorf("read deployed realip state %s: %w", path, err)
	}
	state, err := parseDeployedRealIPState(data, path, profileName)
	if err != nil {
		return realip.State{}, err
	}
	return state, nil
}

func validateDeployedRealIPStateInfo(path string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("deployed realip state %s must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("deployed realip state %s must be a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("deployed realip state %s must be root-only, for example mode 0600", path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return fmt.Errorf("deployed realip state %s owner could not be inspected", path)
	}
	if uid != 0 {
		return fmt.Errorf("deployed realip state %s must be owned by root", path)
	}
	return nil
}

func parseDeployedRealIPState(data []byte, path string, profileName string) (realip.State, error) {
	var managed struct {
		LanpanelManaged string `json:"lanpanel_managed"`
		realip.State
	}
	if err := json.Unmarshal(data, &managed); err != nil {
		return realip.State{}, fmt.Errorf("parse deployed realip state %s: %w", path, err)
	}
	expectedMarker := realIPManagedMarker(profileName, appconfig.RealIPProviderEdgeOne)
	if managed.LanpanelManaged != expectedMarker {
		return realip.State{}, fmt.Errorf("deployed realip state %s has lanpanel_managed marker %q, want %q", path, managed.LanpanelManaged, expectedMarker)
	}
	state := managed.State
	if state.ProfileName != profileName {
		return realip.State{}, fmt.Errorf("deployed realip state %s profile_name %q does not match %q", path, state.ProfileName, profileName)
	}
	if state.Provider != appconfig.RealIPProviderEdgeOne {
		return realip.State{}, fmt.Errorf("deployed realip state %s provider %q is not supported", path, state.Provider)
	}
	if !isSafeDeployedEdgeOneZoneID(state.ZoneID) {
		return realip.State{}, fmt.Errorf("deployed realip state %s zone_id must be an EdgeOne zone id such as zone-xxxxxxxx", path)
	}
	switch strings.TrimSpace(state.OriginACLStatus) {
	case edgeone.OriginACLStatusOnline, edgeone.OriginACLStatusUpdating:
	default:
		return realip.State{}, fmt.Errorf("deployed realip state %s origin_acl_status must be online or updating", path)
	}
	if state.OriginACLStatus == edgeone.OriginACLStatusUpdating && len(state.NextCIDRs) == 0 {
		return realip.State{}, fmt.Errorf("deployed realip state %s next_cidrs is required when origin_acl_status is updating", path)
	}
	if err := validateDeployedRealIPStateCIDRs(path, &state); err != nil {
		return realip.State{}, err
	}
	if state.UpdatedAt.IsZero() {
		return realip.State{}, fmt.Errorf("deployed realip state %s updated_at is required", path)
	}
	return state, nil
}

func validateDeployedRealIPStateCIDRs(path string, state *realip.State) error {
	currentCIDRs, err := realip.CanonicalCIDRs(state.CurrentCIDRs)
	if err != nil {
		return fmt.Errorf("validate deployed realip state %s current_cidrs: %w", path, err)
	}
	nextCIDRs := []string(nil)
	if len(state.NextCIDRs) > 0 {
		nextCIDRs, err = realip.CanonicalCIDRs(state.NextCIDRs)
		if err != nil {
			return fmt.Errorf("validate deployed realip state %s next_cidrs: %w", path, err)
		}
	}
	trustedCIDRs, err := realip.CanonicalCIDRs(state.TrustedCIDRs)
	if err != nil {
		return fmt.Errorf("validate deployed realip state %s trusted_cidrs: %w", path, err)
	}
	expectedTrustedCIDRs, err := realip.CanonicalCIDRs(append(append([]string{}, currentCIDRs...), nextCIDRs...))
	if err != nil {
		return fmt.Errorf("validate deployed realip state %s current_cidrs+next_cidrs: %w", path, err)
	}
	if !stringSlicesEqual(trustedCIDRs, expectedTrustedCIDRs) {
		return fmt.Errorf("deployed realip state %s trusted_cidrs %s must equal current_cidrs+next_cidrs %s", path, listAsNone(trustedCIDRs), listAsNone(expectedTrustedCIDRs))
	}
	state.CurrentCIDRs = currentCIDRs
	state.NextCIDRs = nextCIDRs
	state.TrustedCIDRs = trustedCIDRs
	return nil
}

func isSafeDeployedEdgeOneZoneID(value string) bool {
	if !strings.HasPrefix(value, "zone-") || len(value) <= len("zone-") {
		return false
	}
	for _, r := range value[len("zone-"):] {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func validateDeployedRealIPEnvFile(metadataPath string, envFile string) error {
	if envFile == "" {
		return fmt.Errorf("deployed realip profile metadata %s env_file is required", metadataPath)
	}
	if !filepath.IsAbs(envFile) {
		return fmt.Errorf("deployed realip profile metadata %s env_file must be an absolute path", metadataPath)
	}
	if filepath.Clean(envFile) != envFile {
		return fmt.Errorf("deployed realip profile metadata %s env_file must be a clean path without . or .. segments", metadataPath)
	}
	for _, r := range envFile {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("deployed realip profile metadata %s env_file must not contain whitespace or control characters", metadataPath)
		}
	}
	if strings.ContainsAny(envFile, "*?[]%\"'\\") {
		return fmt.Errorf("deployed realip profile metadata %s env_file must not contain glob, specifier, quote, or escape characters", metadataPath)
	}
	return nil
}

func readDeployedRealIPReferences(dir string) ([]realip.Reference, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read deployed realip references %s: %w", dir, err)
	}
	profileName := strings.TrimSpace(filepath.Base(filepath.Dir(dir)))
	if _, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, ""); err != nil {
		return nil, fmt.Errorf("read deployed realip references %s: invalid profile path: %w", dir, err)
	}
	references := []realip.Reference{}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			return nil, fmt.Errorf("deployed realip references directory %s contains unexpected directory %s", dir, path)
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			return nil, fmt.Errorf("deployed realip references directory %s contains unexpected entry %s; only *.json reference files are supported", dir, path)
		}
		appName := strings.TrimSuffix(entry.Name(), ".json")
		info, err := lstatDeployedRealIPReferenceFn(path)
		if err != nil {
			return nil, fmt.Errorf("stat deployed realip reference %s: %w", path, err)
		}
		if err := validateDeployedRealIPReferenceInfo(path, info); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read deployed realip reference %s: %w", path, err)
		}
		reference, err := parseDeployedRealIPReference(data, path, profileName, appName)
		if err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return references, nil
}

func validateDeployedRealIPReferenceInfo(path string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("deployed realip reference %s must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("deployed realip reference %s must be a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("deployed realip reference %s must be root-only, for example mode 0600", path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return fmt.Errorf("deployed realip reference %s owner could not be inspected", path)
	}
	if uid != 0 {
		return fmt.Errorf("deployed realip reference %s must be owned by root", path)
	}
	return nil
}

func parseDeployedRealIPReference(data []byte, path string, profileName string, appName string) (realip.Reference, error) {
	var managed struct {
		LanpanelManaged string `json:"lanpanel_managed"`
		realip.Reference
	}
	if err := json.Unmarshal(data, &managed); err != nil {
		return realip.Reference{}, fmt.Errorf("parse deployed realip reference %s: %w", path, err)
	}
	expectedMarker := realIPManagedMarker(profileName, appconfig.RealIPProviderEdgeOne)
	if managed.LanpanelManaged != expectedMarker {
		return realip.Reference{}, fmt.Errorf("deployed realip reference %s has lanpanel_managed marker %q, want %q", path, managed.LanpanelManaged, expectedMarker)
	}
	if _, err := appsvc.NewRealIPProfileNames(profileName, appconfig.RealIPProviderEdgeOne, appName); err != nil {
		return realip.Reference{}, fmt.Errorf("deployed realip reference %s filename app name is not safe: %w", path, err)
	}
	if managed.Profile != profileName {
		return realip.Reference{}, fmt.Errorf("deployed realip reference %s profile %q does not match %q", path, managed.Profile, profileName)
	}
	if managed.AppName != appName {
		return realip.Reference{}, fmt.Errorf("deployed realip reference %s app_name %q does not match filename %q", path, managed.AppName, appName+".json")
	}
	cleanDomains := make([]string, 0, len(managed.Domains))
	for _, domain := range managed.Domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			return realip.Reference{}, fmt.Errorf("deployed realip reference %s domains must not contain empty values", path)
		}
		cleanDomains = append(cleanDomains, domain)
	}
	if len(cleanDomains) == 0 {
		return realip.Reference{}, fmt.Errorf("deployed realip reference %s domains must not be empty", path)
	}
	return realip.Reference{AppName: managed.AppName, Profile: managed.Profile, Domains: cleanDomains}, nil
}

func splitAppRealIPStagedFiles(staged []realiprender.StagedFile, referencePath string) ([]realiprender.StagedFile, []realiprender.StagedFile, error) {
	referencePath = strings.TrimSpace(referencePath)
	if referencePath == "" {
		return nil, nil, fmt.Errorf("realip reference path is required")
	}
	shared := make([]realiprender.StagedFile, 0, len(staged))
	reference := []realiprender.StagedFile{}
	for _, file := range staged {
		if file.HostPath == referencePath {
			reference = append(reference, file)
			continue
		}
		shared = append(shared, file)
	}
	if len(reference) != 1 {
		return nil, nil, fmt.Errorf("staged realip reference %s count = %d, want 1", referencePath, len(reference))
	}
	return shared, reference, nil
}

func replaceRealIPReferenceForApp(references []realip.Reference, reference realip.Reference) []realip.Reference {
	replaced := make([]realip.Reference, 0, len(references)+1)
	for _, existing := range references {
		if existing.AppName == reference.AppName {
			continue
		}
		replaced = append(replaced, existing)
	}
	return append(replaced, reference)
}

func domainsFromRealIPReferences(references []realip.Reference) []string {
	seen := map[string]struct{}{}
	domains := []string{}
	for _, reference := range references {
		for _, domain := range reference.Domains {
			domain = strings.TrimSpace(domain)
			if domain == "" {
				continue
			}
			if _, ok := seen[domain]; ok {
				continue
			}
			seen[domain] = struct{}{}
			domains = append(domains, domain)
		}
	}
	return domains
}

func appRealIPRefreshRootRequiredResponse(permissions preflight.PermissionState) output.Response {
	detail := "fail: app realip refresh requires root privileges"
	if strings.TrimSpace(permissions.User) != "" {
		detail += ", current user: " + strings.TrimSpace(permissions.User)
	}
	return output.Response{
		Command:   "app realip refresh",
		Status:    "blocked",
		Summary:   "app realip refresh preflight found 1 failed check",
		Fields:    []output.Field{{Label: "check permissions", Value: detail}},
		NextSteps: []string{"Rerun with sudo lanpanel app realip refresh --profile <name>."},
	}
}

func appRealIPDiagnosticsRootRequiredResponse(permissions preflight.PermissionState) output.Response {
	detail := "fail: app realip diagnostics requires root privileges"
	if strings.TrimSpace(permissions.User) != "" {
		detail += ", current user: " + strings.TrimSpace(permissions.User)
	}
	return output.Response{
		Command:   "app realip diagnostics",
		Status:    "blocked",
		Summary:   "app realip diagnostics preflight found 1 failed check",
		Fields:    []output.Field{{Label: "check permissions", Value: detail}},
		NextSteps: []string{"Rerun with sudo lanpanel app realip diagnostics --profile <name>."},
	}
}

func emptyAsNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

func listAsNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func timeAsNone(value time.Time) string {
	if value.IsZero() {
		return "none"
	}
	return value.UTC().Format(time.RFC3339)
}

func appConfigFields(path string, cfg appconfig.Config) []output.Field {
	return []output.Field{
		{Label: "config path", Value: path},
		{Label: "app name", Value: cfg.App.Name},
		{Label: "mode", Value: string(cfg.Mode())},
		{Label: "domains", Value: strings.Join(cfg.App.Domains, ", ")},
	}
}

func appRuntimeHostChecksStep(cfg appconfig.Config) string {
	return "After deploy, use " + appRuntimeHostCheckTools(cfg) + " to verify host runtime state."
}

func appDeployRuntimeVerifyStep(configPath string, cfg appconfig.Config) string {
	return fmt.Sprintf("Run 'lanpanel app verify --config %s' to recheck static config and templates; then use %s to verify host runtime state.", configPath, appRuntimeHostCheckTools(cfg))
}

func appRuntimeHostCheckTools(cfg appconfig.Config) string {
	if cfg.RequiresTailscale() {
		return "nginx -t, systemctl, certificate checks, curl, and tailscale status"
	}
	return "nginx -t, systemctl, certificate checks, and curl"
}

func appGoAccessDeployFields(cfg appconfig.Config, names appsvc.Names) []output.Field {
	if !cfg.Nginx.GoAccess.Enabled {
		return nil
	}
	errorLog := strings.TrimSpace(cfg.Nginx.ErrorLog)
	if errorLog == "" {
		errorLog = "nginx default error_log"
	}
	return []output.Field{
		{Label: "goaccess dashboard", Value: "https://" + cfg.PrimaryDomain() + cfg.NginxGoAccessDashboardPath()},
		{Label: "goaccess service", Value: names.GoAccessServiceUnit},
		{Label: "canonical access log", Value: names.GoAccessCanonicalAccessLogPath},
		{Label: "goaccess report", Value: names.GoAccessReportPath},
		{Label: "goaccess db", Value: names.GoAccessDBPath},
		{Label: "nginx error log", Value: errorLog},
		{Label: "goaccess troubleshooting", Value: appGoAccessTroubleshootingCommands(cfg, names)},
		{Label: "goaccess dashboard scope", Value: appGoAccessDashboardScope(cfg)},
	}
}

func appRealIPDeployFields(cfg appconfig.Config, names appsvc.RealIPProfileNames, state realip.State, prepared bool) []output.Field {
	if !prepared || !cfg.RealIPEnabled() {
		return nil
	}
	fields := []output.Field{
		{Label: "realip profile", Value: names.ProfileName + " (" + names.Provider + ")"},
		{Label: "manual firewall confirmation", Value: "confirm cloud security group or host firewall allows only EdgeOne current+next origin ACL CIDRs to 80/443"},
	}
	fields = append(fields, realIPRuntimeFields(names, state, "nginx -t passed and Nginx reloaded with app site")...)
	return fields
}

func realIPRuntimeFields(names appsvc.RealIPProfileNames, state realip.State, nginxStatus string) []output.Field {
	return []output.Field{
		{Label: "realip nginx include", Value: names.NginxIncludePath},
		{Label: "realip trusted CIDR include", Value: names.TrustedCIDRPath},
		{Label: "realip current CIDRs", Value: listAsNone(state.CurrentCIDRs)},
		{Label: "realip next CIDRs", Value: listAsNone(state.NextCIDRs)},
		{Label: "realip trusted CIDRs", Value: listAsNone(state.TrustedCIDRs)},
		{Label: "realip state", Value: names.StatePath},
		{Label: "realip refresh timer", Value: names.RefreshTimerUnit},
		{Label: "realip updated at", Value: timeAsNone(state.UpdatedAt)},
		{Label: "origin acl status", Value: emptyAsNone(state.OriginACLStatus)},
		{Label: "origin acl family", Value: emptyAsNone(state.OriginACLFamily)},
		{Label: "current acl version", Value: emptyAsNone(state.CurrentVersion)},
		{Label: "current active time", Value: emptyAsNone(state.CurrentActiveTime)},
		{Label: "next acl version", Value: emptyAsNone(state.NextVersion)},
		{Label: "next active time", Value: emptyAsNone(state.NextActiveTime)},
		{Label: "planned active time", Value: emptyAsNone(state.PlannedActiveTime)},
		{Label: "nginx realip active", Value: nginxStatus},
	}
}

func summarizeRealIPReferences(references []realip.Reference) string {
	if len(references) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(references))
	for _, reference := range references {
		parts = append(parts, reference.AppName+"="+strings.Join(reference.Domains, ","))
	}
	return strings.Join(parts, "; ")
}

func deployedRealIPRegularFileStatus(path string) (string, error) {
	info, err := lstatDeployedRealIPArtifactFn(path)
	if err != nil {
		return "missing: " + err.Error(), fmt.Errorf("stat deployed realip artifact %s: %w", path, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "invalid: symlink", fmt.Errorf("deployed realip artifact %s must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return "invalid: not a regular file", fmt.Errorf("deployed realip artifact %s must be a regular file", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Sprintf("invalid: mode %04o is group/other writable", info.Mode().Perm()), fmt.Errorf("deployed realip artifact %s must not be writable by group or others", path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return "invalid: owner could not be inspected", fmt.Errorf("deployed realip artifact %s owner could not be inspected", path)
	}
	if uid != 0 {
		return fmt.Sprintf("invalid: owner uid %d", uid), fmt.Errorf("deployed realip artifact %s must be owned by root", path)
	}
	return fmt.Sprintf("present root-owned mode %04o", info.Mode().Perm()), nil
}

func validateDeployedRealIPNginxInclude(path string, marker string, expectedCIDRs []string) error {
	content, err := readDeployedRealIPArtifactFn(path)
	if err != nil {
		return fmt.Errorf("read deployed realip artifact %s: %w", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "# "+marker) {
		return fmt.Errorf("deployed realip artifact %s missing marker %q", path, marker)
	}
	cidrs, err := parseSetRealIPFromCIDRs(path, text)
	if err != nil {
		return err
	}
	return requireCIDRListMatch(path, cidrs, expectedCIDRs)
}

func validateDeployedRealIPTrustedCIDRInclude(path string, marker string, expectedCIDRs []string) error {
	content, err := readDeployedRealIPArtifactFn(path)
	if err != nil {
		return fmt.Errorf("read deployed realip artifact %s: %w", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "# "+marker) {
		return fmt.Errorf("deployed realip artifact %s missing marker %q", path, marker)
	}
	cidrs, err := parseTrustedCIDRGeoEntries(path, text)
	if err != nil {
		return err
	}
	return requireCIDRListMatch(path, cidrs, expectedCIDRs)
}

func validateDeployedRealIPRefreshService(path string, marker string, profileName string) error {
	text, err := readDeployedRealIPArtifactText(path)
	if err != nil {
		return err
	}
	if err := requireDeployedRealIPMarker(path, text, marker); err != nil {
		return err
	}
	for _, line := range []string{
		"Type=oneshot",
		"TimeoutStartSec=2min",
		"ExecStart=" + realipassets.DefaultRefreshBinaryPath + " app realip refresh --profile " + strings.TrimSpace(profileName),
	} {
		if !hasTrimmedLine(text, line) {
			return fmt.Errorf("deployed realip artifact %s missing line %q", path, line)
		}
	}
	return nil
}

func validateDeployedRealIPRefreshTimer(path string, marker string, profile realip.ProfileConfig) error {
	text, err := readDeployedRealIPArtifactText(path)
	if err != nil {
		return err
	}
	if err := requireDeployedRealIPMarker(path, text, marker); err != nil {
		return err
	}
	refreshInterval, err := realip.SystemdRefreshInterval(profile.RefreshInterval)
	if err != nil {
		return fmt.Errorf("derive deployed realip refresh timer interval for %s: %w", path, err)
	}
	for _, line := range []string{
		"OnBootSec=15m",
		"OnUnitActiveSec=" + refreshInterval,
		"RandomizedDelaySec=30m",
		"Persistent=true",
		"WantedBy=timers.target",
	} {
		if !hasTrimmedLine(text, line) {
			return fmt.Errorf("deployed realip artifact %s missing line %q", path, line)
		}
	}
	return nil
}

func readDeployedRealIPArtifactText(path string) (string, error) {
	content, err := readDeployedRealIPArtifactFn(path)
	if err != nil {
		return "", fmt.Errorf("read deployed realip artifact %s: %w", path, err)
	}
	return string(content), nil
}

func requireDeployedRealIPMarker(path string, text string, marker string) error {
	if !strings.Contains(text, "# "+marker) {
		return fmt.Errorf("deployed realip artifact %s missing marker %q", path, marker)
	}
	return nil
}

func hasTrimmedLine(text string, want string) bool {
	for _, raw := range strings.Split(text, "\n") {
		if strings.TrimSpace(raw) == want {
			return true
		}
	}
	return false
}

func parseSetRealIPFromCIDRs(path string, text string) ([]string, error) {
	cidrs := []string{}
	for lineNumber, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		value, ok := strings.CutPrefix(line, "set_real_ip_from ")
		if !ok || !strings.HasSuffix(value, ";") {
			return nil, fmt.Errorf("deployed realip artifact %s line %d must be set_real_ip_from CIDR;", path, lineNumber+1)
		}
		cidrs = append(cidrs, strings.TrimSpace(strings.TrimSuffix(value, ";")))
	}
	return cidrs, nil
}

func parseTrustedCIDRGeoEntries(path string, text string) ([]string, error) {
	cidrs := []string{}
	for lineNumber, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != "1;" {
			return nil, fmt.Errorf("deployed realip artifact %s line %d must be CIDR 1;", path, lineNumber+1)
		}
		cidrs = append(cidrs, fields[0])
	}
	return cidrs, nil
}

func requireCIDRListMatch(path string, actual []string, expected []string) error {
	if _, err := realip.CanonicalCIDRs(actual); err != nil {
		return fmt.Errorf("validate deployed realip artifact %s CIDRs: %w", path, err)
	}
	if !stringSlicesEqual(actual, expected) {
		return fmt.Errorf("deployed realip artifact %s CIDRs %s do not match deployed state trusted CIDRs %s", path, listAsNone(actual), listAsNone(expected))
	}
	return nil
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func appGoAccessDeployNextSteps(cfg appconfig.Config, names appsvc.Names) []string {
	if !cfg.Nginx.GoAccess.Enabled {
		return nil
	}
	return []string{
		"Open https://" + cfg.PrimaryDomain() + cfg.NginxGoAccessDashboardPath() + " and sign in with the account from nginx.goaccess.auth_basic_user_file to verify the GoAccess dashboard.",
		appGoAccessFailureNextStep(cfg),
		"Common commands: " + appGoAccessTroubleshootingCommands(cfg, names),
	}
}

func appGoAccessFailureNextStep(cfg appconfig.Config) string {
	if cfg.Mode() == appconfig.ModeUpstream {
		return "For 5xx, upstream timeout, TLS, or permission denied issues, continue checking nginx.error_log, fixed tailnet upstream reachability, and remote service logs; the GoAccess dashboard does not parse Nginx error logs."
	}
	return "For 5xx, upstream timeout, TLS, or permission denied issues, continue checking nginx.error_log and the business service logs; the GoAccess dashboard does not parse Nginx error logs."
}

func appGoAccessTroubleshootingCommands(cfg appconfig.Config, names appsvc.Names) string {
	commands := []string{
		"systemctl status " + names.GoAccessServiceUnit + " --no-pager --full",
		"journalctl -u " + names.GoAccessServiceUnit + " -e",
		"tail -f " + names.GoAccessCanonicalAccessLogPath,
	}
	if errorLog := strings.TrimSpace(cfg.Nginx.ErrorLog); errorLog != "" {
		commands = append(commands, "tail -f "+errorLog)
	} else {
		commands = append(commands, "journalctl -u nginx.service -e", "tail -f /var/log/nginx/error.log")
	}
	if cfg.Mode() == appconfig.ModeListen {
		commands = append(commands, "journalctl -u "+names.ServiceUnit+" -e")
	}
	if cfg.RequiresTailscale() {
		commands = append(commands, "tailscale status")
	}
	if cfg.Mode() == appconfig.ModeUpstream {
		commands = append(commands, "curl -I http://"+cfg.App.Upstream)
	}
	return strings.Join(commands, "; ")
}

func appGoAccessDashboardScope(cfg appconfig.Config) string {
	scope := "request volume, visitors, URLs, 404/status codes, IP/Host, referrer, User-Agent/browser/OS, bandwidth, visit time"
	if cfg.Nginx.GoAccess.EffectiveLogFormat() == appconfig.NginxGoAccessLogFormatEnhanced {
		scope += ", request serving time"
	}
	return scope + "; upstream fields and nginx.error_log remain raw-log/troubleshooting inputs, not first-class GoAccess panels"
}

func appPreflightFields(report apppreflight.Report) []output.Field {
	fields := make([]output.Field, 0, len(report.Checks))
	for _, check := range report.Checks {
		fields = append(fields, output.Field{Label: "check " + check.ID, Value: string(check.Status) + ": " + check.Summary})
	}
	return fields
}

func appPreflightWarningFields(report apppreflight.Report) []output.Field {
	fields := []output.Field{}
	for _, check := range report.Checks {
		if check.Status != apppreflight.StatusWarn {
			continue
		}
		fields = append(fields, output.Field{Label: "preflight warning " + check.ID, Value: check.Summary})
	}
	return fields
}

func appPreflightWarningNextSteps(report apppreflight.Report) []string {
	seen := map[string]struct{}{}
	steps := []string{}
	for _, check := range report.Checks {
		if check.Status != apppreflight.StatusWarn {
			continue
		}
		for _, remediation := range check.Remediations {
			if strings.TrimSpace(remediation) == "" {
				continue
			}
			if _, ok := seen[remediation]; ok {
				continue
			}
			seen[remediation] = struct{}{}
			steps = append(steps, remediation)
		}
	}
	return steps
}

func detectAppDNS(cfg appconfig.Config) map[string]preflight.DNSProbe {
	probes := make(map[string]preflight.DNSProbe, len(cfg.App.Domains))
	expectedIPv4, expectedIPv6 := detectAppExpectedPublicIPs(cfg)
	for _, domain := range cfg.App.Domains {
		resolved, err := net.LookupHost(domain)
		probe := preflight.DNSProbe{Host: domain, ResolvedIPs: resolved, ExpectedIPv4: expectedIPv4, ExpectedIPv6: expectedIPv6}
		if err != nil {
			probe.LookupError = err.Error()
		}
		probes[domain] = probe
	}
	return probes
}

func detectAppExpectedPublicIPs(cfg appconfig.Config) (string, string) {
	var client *http.Client
	mainConfigPath := cfg.EffectiveLanpanelConfig()
	if strings.TrimSpace(mainConfigPath) != "" {
		mainCfg, err := config.LoadFile(mainConfigPath)
		if err == nil {
			expectedIPv4 := strings.TrimSpace(mainCfg.Advanced.Network.PublicIPv4)
			expectedIPv6 := strings.TrimSpace(mainCfg.Advanced.Network.PublicIPv6)
			if expectedIPv4 != "" || expectedIPv6 != "" {
				return expectedIPv4, expectedIPv6
			}
			client = newDeployHTTPClient(mainCfg.Advanced.Proxy, 3*time.Second)
		}
	}
	return detectAppCurrentPublicIPsFn(client)
}

func detectAppCurrentPublicIPs(client *http.Client) (string, string) {
	return detectAppPublicIP(client, "https://api4.ipify.org"), detectAppPublicIP(client, "https://api6.ipify.org")
}

func detectAppPublicIP(client *http.Client, endpoint string) string {
	client = httpClientOrDefault(client, 3*time.Second)
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("User-Agent", "lanpanel-app-preflight/1.0")
	response, err := client.Do(request)
	if err != nil {
		return ""
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(data))
	ip, err := netip.ParseAddr(value)
	if err != nil || !isAppExpectedPublicIP(ip) {
		return ""
	}
	return ip.Unmap().String()
}

func isAppExpectedPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range appNonPublicRoutableIPPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func detectAppPortBindings() []preflight.PortBinding {
	tcpBindings, tcpDetected := detectSSBindingList("tcp", []int{80, 443})
	if !tcpDetected {
		return nil
	}
	bindings := make([]preflight.PortBinding, 0, len(tcpBindings)+2)
	for _, port := range []int{80, 443} {
		found := false
		for _, binding := range tcpBindings {
			if binding.Port != port || !strings.EqualFold(binding.Protocol, "tcp") {
				continue
			}
			bindings = append(bindings, binding)
			found = true
		}
		if found {
			continue
		}
		bindings = append(bindings, preflight.PortBinding{Port: port, Protocol: "tcp"})
	}
	return bindings
}

func detectAppListenPortState(cfg appconfig.Config) (bool, bool, string) {
	if cfg.Mode() != appconfig.ModeListen {
		return false, false, ""
	}
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		return true, false, err.Error()
	}
	appHost, appPort, ok := splitAppHostPort(cfg.App.Listen)
	if !ok {
		return true, false, "app.listen must be in host:port format"
	}
	bindings, detected := detectSSBindingList("tcp", []int{appPort})
	if !detected {
		return true, false, "Could not confirm app.listen port usage"
	}
	overlapping := make([]preflight.PortBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Port != appPort || !binding.InUse {
			continue
		}
		if !socketBindHostsOverlap(binding.LocalAddress, appHost) {
			continue
		}
		overlapping = append(overlapping, binding)
	}
	if len(overlapping) == 0 {
		return true, true, "app.listen " + cfg.App.Listen + " is available"
	}
	for _, binding := range overlapping {
		if strings.TrimSpace(binding.Process) == "" || binding.PID <= 0 {
			return true, false, "Could not confirm whether app.listen " + cfg.App.Listen + " is already used by current managed app or GoAccess service"
		}
		appManaged, appConfirmed := isAppManagedPortBindingFn(names, binding)
		if appConfirmed && appManaged {
			continue
		}
		goAccessManaged, goAccessConfirmed := isAppGoAccessManagedPortBindingFn(names, binding)
		if goAccessConfirmed && goAccessManaged {
			continue
		}
		if !appConfirmed {
			return true, false, "Could not confirm whether app.listen " + cfg.App.Listen + " is already used by current app service " + names.ServiceUnit
		}
		if !goAccessConfirmed {
			return true, false, "Could not confirm whether app.listen " + cfg.App.Listen + " is already used by current GoAccess service " + names.GoAccessServiceUnit
		}
		process := strings.TrimSpace(binding.Process)
		if process == "" {
			process = "unknown process"
		}
		return true, false, "app.listen " + cfg.App.Listen + " is already used by " + process
	}
	return true, true, "app.listen " + cfg.App.Listen + " is already used by current managed app or GoAccess service; deploy will refresh the owning service"
}

func detectAppDNSCredentialState(cfg appconfig.Config) (bool, bool, string) {
	if cfg.App.ACMEChallenge != appconfig.ACMEChallengeDNS01 {
		return false, false, ""
	}
	providerInfo, err := acme.DNSProvider(cfg.DNS01.Provider)
	if err != nil {
		return false, false, err.Error()
	}

	envFile := strings.TrimSpace(cfg.DNS01.EnvFile)
	if envFile != "" {
		env, ready, detail := inspectAppDNSEnvFile(envFile)
		if !ready {
			return true, false, fmt.Sprintf("DNS provider %q env_file is not ready: %s", providerInfo.LegoCode, detail)
		}
		if env != nil {
			if err := acme.ValidateDNSProviderEnvironment(providerInfo.LegoCode, env); err != nil {
				return true, false, strings.ReplaceAll(err.Error(), "advanced.dns01", "dns01")
			}
			if ready, detail := inspectAppDNSCredentialEnvFileReferences(providerInfo.LegoCode, env); !ready {
				return true, false, fmt.Sprintf("DNS provider %q env_file contains credential file references that are not ready: %s.", providerInfo.LegoCode, detail)
			}
		}
		return true, true, fmt.Sprintf("Using lego env_file for DNS provider %q: %s. %s", providerInfo.LegoCode, envFile, detail)
	}

	env := nonEmptyEnvironmentByKey()
	if providerInfo.LegoCode == "route53" && route53RawSecretEnvironmentPresent(env) && strings.TrimSpace(env["AWS_SHARED_CREDENTIALS_FILE"]) == "" {
		return true, false, "Detected Route53 AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY in the current environment, but lanpanel app deploy will not pass raw AWS secrets through sudo or systemd. Use dns01.env_file for DNS-01 deploy and renewal."
	}
	if providerInfo.AmbientCredentialsSupported {
		detail := fmt.Sprintf("Using lego ambient credential chain for DNS provider %q; confirm deploy and %s run with the same host identity.", providerInfo.LegoCode, cfg.App.Name+"-lego-renew.service")
		if providerInfo.LegoCode == "gcloud" {
			detail += " For gcloud, confirm Google Cloud metadata also provides the project, or set dns01.env_file with GCE_PROJECT."
		}
		return true, true, detail
	}
	return true, false, fmt.Sprintf("DNS provider %q requires dns01.env_file so initial issuance and app lego renewal use the same provider environment.", providerInfo.LegoCode)
}

func inspectAppDNSEnvFile(filePath string) (map[string]string, bool, string) {
	ready, detail := inspectAppRootOnlyFile("dns01.env_file", filePath)
	if !ready {
		return nil, false, detail
	}
	content, readDetail, err := readDNSCredentialEnvFile(filePath)
	if err != nil {
		return nil, false, fmt.Sprintf("%s cannot be opened for validation: %s.", filePath, err)
	}
	if readDetail != "" {
		detail = strings.TrimSpace(detail + " " + readDetail)
	}
	env, syntaxDetail := parseDNSEnvFileContent(content)
	if syntaxDetail != "" {
		return env, false, fmt.Sprintf("%s contains unsupported syntax for systemd EnvironmentFile: %s. Use KEY=value lines without export.", filePath, syntaxDetail)
	}
	if len(env) == 0 {
		return env, false, fmt.Sprintf("%s does not contain any KEY=value environment assignments.", filePath)
	}
	return env, true, strings.ReplaceAll(detail, "advanced.dns01", "dns01")
}

func inspectAppDNSCredentialEnvFileReferences(provider string, env map[string]string) (bool, string) {
	invalidDetails := []string{}
	for key, value := range env {
		if !dnsCredentialEnvironmentValueIsFile(provider, key) {
			continue
		}
		if !filepath.IsAbs(value) {
			invalidDetails = append(invalidDetails, fmt.Sprintf("%s: referenced credential file path %q must be absolute so deploy and systemd renewal use the same runtime path", key, value))
			continue
		}
		ready, detail := inspectAppRootOnlyFile(key, value)
		if !ready {
			invalidDetails = append(invalidDetails, fmt.Sprintf("%s: %s", key, detail))
		}
	}
	invalidDetails = uniqueStrings(invalidDetails)
	if len(invalidDetails) > 0 {
		return false, strings.Join(invalidDetails, "; ")
	}
	return true, ""
}

func detectAppServiceEnvFileState(cfg appconfig.Config) (bool, bool, string) {
	path := strings.TrimSpace(cfg.Service.EnvFile)
	if cfg.Mode() != appconfig.ModeListen || path == "" {
		return false, false, ""
	}
	ready, detail := inspectAppRootOnlyFile("service.env_file", path)
	if !ready {
		return true, false, detail
	}
	return true, true, "service.env_file passed root-only validation"
}

func detectAppTailscaleAuthKeyFileState(cfg appconfig.Config) (bool, bool, string) {
	path := strings.TrimSpace(cfg.Tailscale.AuthKeyFile)
	if path == "" {
		return false, false, ""
	}
	if _, err := tailscalecomponent.ReadAuthKeyFile(path); err != nil {
		return true, false, err.Error()
	}
	return true, true, "tailscale.auth_key_file passed root-only validation"
}

func detectAppGoAccessAuthFileState(cfg appconfig.Config) (bool, bool, string) {
	if !cfg.Nginx.GoAccess.Enabled {
		return false, false, ""
	}
	path := strings.TrimSpace(cfg.Nginx.GoAccess.AuthBasicUserFile)
	if path == "" {
		return true, false, "nginx.goaccess.auth_basic_user_file is required"
	}
	info, err := lstatAppServicePathFn(path)
	if err != nil {
		return true, false, "nginx.goaccess.auth_basic_user_file unavailable: " + err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true, false, "nginx.goaccess.auth_basic_user_file must not be a symlink"
	}
	if !info.Mode().IsRegular() {
		return true, false, "nginx.goaccess.auth_basic_user_file must be a regular file"
	}
	if info.Size() == 0 {
		return true, false, "nginx.goaccess.auth_basic_user_file must not be empty"
	}
	if uid, ok := fileOwnerUID(info); ok && uid != 0 {
		return true, false, "nginx.goaccess.auth_basic_user_file must be owned by root"
	}
	if info.Mode().Perm()&0o020 != 0 || info.Mode().Perm()&0o007 != 0 {
		return true, false, "nginx.goaccess.auth_basic_user_file must not be group-writable or accessible by others"
	}
	if err := validateGoAccessAuthBasicUserFileContent(path); err != nil {
		return true, false, err.Error()
	}
	if err := validateAppRootOwnedFileParents("nginx.goaccess.auth_basic_user_file", path, false); err != nil {
		return true, false, err.Error()
	}
	return true, true, "nginx.goaccess.auth_basic_user_file passed static path, permission, content, and parent-directory safety checks; Nginx runtime readability will be checked after host dependencies are installed"
}

func detectAppGoAccessPortState(cfg appconfig.Config) (bool, bool, string) {
	if !cfg.Nginx.GoAccess.Enabled {
		return false, false, ""
	}
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		return true, false, err.Error()
	}
	bindings, detected := detectSSBindingList("tcp", []int{names.GoAccessWebSocketPort})
	if !detected {
		return true, false, "Could not confirm GoAccess WebSocket port usage"
	}
	overlapping := make([]preflight.PortBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Port != names.GoAccessWebSocketPort || !binding.InUse {
			continue
		}
		if !socketBindHostsOverlap(binding.LocalAddress, names.GoAccessWebSocketHost) {
			continue
		}
		overlapping = append(overlapping, binding)
	}
	if len(overlapping) == 0 {
		return true, true, fmt.Sprintf("GoAccess WebSocket loopback port %d is available", names.GoAccessWebSocketPort)
	}
	for _, binding := range overlapping {
		appManaged, appConfirmed := isAppManagedPortBindingFn(names, binding)
		if appConfirmed && appManaged {
			continue
		}
		managed, confirmed := isAppGoAccessManagedPortBindingFn(names, binding)
		if confirmed && managed {
			continue
		}
		if !appConfirmed {
			return true, false, fmt.Sprintf("Could not confirm whether GoAccess WebSocket port %d is already used by current app service %s", names.GoAccessWebSocketPort, names.ServiceUnit)
		}
		if !confirmed {
			return true, false, fmt.Sprintf("Could not confirm whether GoAccess WebSocket port %d is already used by current GoAccess service %s", names.GoAccessWebSocketPort, names.GoAccessServiceUnit)
		}
		process := strings.TrimSpace(binding.Process)
		if process == "" {
			process = "unknown process"
		}
		return true, false, fmt.Sprintf("GoAccess WebSocket port %d is already used by %s", names.GoAccessWebSocketPort, process)
	}
	return true, true, fmt.Sprintf("GoAccess WebSocket port %d is already used by current managed app or GoAccess service (%s); deploy will refresh the owning service", names.GoAccessWebSocketPort, names.GoAccessServiceUnit)
}

func detectAppGoAccessAppListenBlockers(cfg appconfig.Config, names appsvc.Names) ([]preflight.PortBinding, bool) {
	if cfg.Mode() != appconfig.ModeListen {
		return nil, true
	}
	appHost, appPort, ok := splitAppHostPort(cfg.App.Listen)
	if !ok {
		return nil, false
	}
	bindings, detected := detectSSBindingList("tcp", []int{appPort})
	if !detected {
		return nil, false
	}
	blockers := make([]preflight.PortBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Port != appPort || !binding.InUse {
			continue
		}
		if !socketBindHostsOverlap(binding.LocalAddress, appHost) {
			continue
		}
		if strings.TrimSpace(binding.Process) == "" || binding.PID <= 0 {
			return nil, false
		}
		appManaged, appConfirmed := isAppManagedPortBindingFn(names, binding)
		if appConfirmed && appManaged {
			continue
		}
		managed, confirmed := isAppGoAccessManagedPortBindingFn(names, binding)
		if confirmed && managed {
			blockers = append(blockers, binding)
			continue
		}
		if !appConfirmed || !confirmed {
			return nil, false
		}
		return nil, false
	}
	return blockers, true
}

func detectAppGoAccessAppPortBlockers(cfg appconfig.Config, names appsvc.Names) ([]preflight.PortBinding, bool) {
	if !cfg.Nginx.GoAccess.Enabled {
		return nil, true
	}
	bindings, detected := detectSSBindingList("tcp", []int{names.GoAccessWebSocketPort})
	if !detected {
		return nil, false
	}
	blockers := make([]preflight.PortBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Port != names.GoAccessWebSocketPort || !binding.InUse {
			continue
		}
		if !socketBindHostsOverlap(binding.LocalAddress, names.GoAccessWebSocketHost) {
			continue
		}
		if strings.TrimSpace(binding.Process) == "" || binding.PID <= 0 {
			return nil, false
		}
		managed, confirmed := isAppManagedPortBindingFn(names, binding)
		if confirmed && managed {
			blockers = append(blockers, binding)
			continue
		}
		goAccessManaged, goAccessConfirmed := isAppGoAccessManagedPortBindingFn(names, binding)
		if goAccessConfirmed && goAccessManaged {
			continue
		}
		if !confirmed || !goAccessConfirmed {
			return nil, false
		}
		return nil, false
	}
	return blockers, true
}

func validateGoAccessAuthBasicUserFileContent(path string) error {
	data, err := readAppServicePathFn(path)
	if err != nil {
		return fmt.Errorf("nginx.goaccess.auth_basic_user_file cannot be opened for validation: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if goAccessAuthLineIsBlankOrComment(line) {
			continue
		}
		if goAccessAuthLineHasCredential(line) {
			return nil
		}
	}
	return fmt.Errorf("nginx.goaccess.auth_basic_user_file must contain at least one user:hash credential line")
}

func goAccessAuthLineIsBlankOrComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

func goAccessAuthLineHasCredential(line string) bool {
	if startsWithSpace(line) {
		return false
	}
	user, hash, ok := strings.Cut(line, ":")
	if !ok || user == "" || hash == "" {
		return false
	}
	if strings.IndexFunc(user, unicode.IsSpace) >= 0 {
		return false
	}
	return strings.IndexFunc(hash, unicode.IsSpace) < 0
}

func startsWithSpace(value string) bool {
	for _, r := range value {
		return unicode.IsSpace(r)
	}
	return false
}

func socketBindHostsOverlap(left string, right string) bool {
	left = normalizeSocketBindHost(left)
	right = normalizeSocketBindHost(right)
	if left == "" || right == "" {
		return true
	}
	if left == "*" || right == "*" {
		return true
	}
	if socketBindHostIsWildcard(left) {
		return socketBindWildcardOverlapsHost(left, right)
	}
	if socketBindHostIsWildcard(right) {
		return socketBindWildcardOverlapsHost(right, left)
	}
	if left == right {
		return true
	}
	if left == "localhost" {
		return socketBindHostIsLoopback(right)
	}
	if right == "localhost" {
		return socketBindHostIsLoopback(left)
	}
	leftIP, leftOK := parseSocketBindIP(left)
	rightIP, rightOK := parseSocketBindIP(right)
	return leftOK && rightOK && leftIP.Compare(rightIP) == 0
}

func socketBindWildcardOverlapsHost(wildcard string, host string) bool {
	switch wildcard {
	case "0.0.0.0":
		return socketBindHostIsIPv4(host) || host == "localhost"
	case "::":
		return true
	default:
		return true
	}
}

func socketBindHostIsIPv4(host string) bool {
	ip, ok := parseSocketBindIP(host)
	return ok && ip.Is4()
}

func socketBindHostIsIPv6(host string) bool {
	ip, ok := parseSocketBindIP(host)
	return ok && ip.Is6()
}

func normalizeSocketBindHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if zoneIndex := strings.Index(host, "%"); zoneIndex >= 0 {
		host = host[:zoneIndex]
	}
	return host
}

func socketBindHostIsWildcard(host string) bool {
	switch host {
	case "*", "0.0.0.0", "::":
		return true
	default:
		return false
	}
}

func socketBindHostIsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, ok := parseSocketBindIP(host)
	return ok && ip.IsLoopback()
}

func parseSocketBindIP(host string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func isAppGoAccessManagedPortBinding(names appsvc.Names, binding preflight.PortBinding) (bool, bool) {
	process := strings.TrimSpace(binding.Process)
	if process == "" || binding.PID <= 0 {
		return false, false
	}
	if !strings.EqualFold(process, "goaccess") {
		return false, true
	}
	return isLanpanelManagedSystemdUnitPortBinding(names.AppName, names.GoAccessServiceUnit, binding)
}

func isAppManagedPortBinding(names appsvc.Names, binding preflight.PortBinding) (bool, bool) {
	if strings.TrimSpace(binding.Process) == "" || binding.PID <= 0 {
		return false, false
	}
	return isLanpanelManagedSystemdUnitPortBinding(names.AppName, names.ServiceUnit, binding)
}

func isLanpanelManagedSystemdUnitPortBinding(appName string, unit string, binding preflight.PortBinding) (bool, bool) {
	if binding.PID <= 0 {
		return false, false
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, false
	}
	unitPath := filepath.Join("/etc/systemd/system", unit)
	content, err := readAppServiceUnitFileFn(unitPath)
	if err != nil {
		return false, false
	}
	if err := appsvc.CheckManagedContent(appName, content); err != nil {
		return false, true
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", unit).Run(); err != nil {
		return false, false
	}
	mainPID, ok := systemdUnitMainPID(unit)
	if !ok {
		return false, false
	}
	if binding.PID == mainPID {
		return true, true
	}
	controlGroup, ok := systemdUnitControlGroup(unit)
	if !ok {
		return false, false
	}
	cgroupContent, err := readAppProcessCgroupFileFn(filepath.Join("/proc", strconv.Itoa(binding.PID), "cgroup"))
	if err != nil {
		return false, false
	}
	return processCgroupContainsSystemdControlGroup(cgroupContent, controlGroup), true
}

func systemdUnitMainPID(unit string) (int, bool) {
	output, err := exec.Command("systemctl", "show", unit, "--property=MainPID", "--value").Output()
	if err != nil {
		return 0, false
	}
	mainPID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || mainPID <= 0 {
		return 0, false
	}
	return mainPID, true
}

func systemdUnitControlGroup(unit string) (string, bool) {
	output, err := exec.Command("systemctl", "show", unit, "--property=ControlGroup", "--value").Output()
	if err != nil {
		return "", false
	}
	controlGroup := strings.TrimSpace(string(output))
	if controlGroup == "" || !strings.HasPrefix(controlGroup, "/") {
		return "", false
	}
	return controlGroup, true
}

func nonEmptyLines(text string) []string {
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func processCgroupContainsSystemdControlGroup(content []byte, controlGroup string) bool {
	controlGroup = strings.TrimRight(strings.TrimSpace(controlGroup), "/")
	if controlGroup == "" {
		return false
	}
	for _, line := range nonEmptyLines(string(content)) {
		_, path, ok := strings.Cut(strings.TrimSpace(line), "::")
		if !ok {
			parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
			if len(parts) != 3 {
				continue
			}
			path = parts[2]
		}
		path = strings.TrimRight(strings.TrimSpace(path), "/")
		if path == controlGroup || strings.HasPrefix(path, controlGroup+"/") {
			return true
		}
	}
	return false
}

func detectAppGoAccessLocaleState(cfg appconfig.Config) (bool, bool, string) {
	if !cfg.Nginx.GoAccess.Enabled {
		return false, false, ""
	}
	language := cfg.Nginx.GoAccess.EffectiveLanguage()
	output, err := exec.Command("locale", "-a").Output()
	if err != nil {
		return true, false, "Could not read locale -a output: " + err.Error()
	}
	required := []struct {
		prefix string
		label  string
	}{
		{prefix: "c", label: "C.UTF-8"},
	}
	if language == appconfig.NginxGoAccessLanguageSimplifiedChinese {
		required = append(required, struct {
			prefix string
			label  string
		}{prefix: "zh_cn", label: "zh_CN.UTF-8"})
	}
	missing := make([]string, 0)
	for _, locale := range required {
		if !hasLocaleUTF8Line(string(output), locale.prefix) {
			missing = append(missing, locale.label)
		}
	}
	if len(missing) > 0 {
		return true, false, "GoAccess language " + language + " requires locale(s): " + strings.Join(missing, ", ")
	}
	return true, true, "GoAccess language " + language + " locale is available"
}

func hasLocaleUTF8Line(output string, localePrefix string) bool {
	want := strings.ToLower(strings.TrimSpace(localePrefix)) + ".utf8"
	for _, line := range strings.Split(output, "\n") {
		normalized := strings.ToLower(strings.TrimSpace(line))
		normalized = strings.ReplaceAll(normalized, "-", "")
		if normalized == want {
			return true
		}
	}
	return false
}

func detectAppGoAccessLogFileState(cfg appconfig.Config) (bool, bool, string) {
	if !cfg.Nginx.GoAccess.Enabled || strings.TrimSpace(cfg.Nginx.AccessLog) == "" || appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
		return false, false, ""
	}
	path := strings.TrimSpace(cfg.Nginx.AccessLog)
	info, err := lstatAppServicePathFn(path)
	if err != nil {
		return true, false, "explicit nginx.access_log unavailable: " + err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true, false, "explicit nginx.access_log must not be a symlink"
	}
	if !info.Mode().IsRegular() {
		return true, false, "explicit nginx.access_log must be a regular file"
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return true, false, "explicit nginx.access_log owner could not be inspected"
	}
	if uid != 0 && uid != 33 {
		return true, false, "explicit nginx.access_log must be owned by root or www-data"
	}
	if info.Mode().Perm()&0o022 != 0 {
		return true, false, "explicit nginx.access_log must not be writable by group or others"
	}
	if err := validateAppRootOwnedFileParents("explicit nginx.access_log", path, false); err != nil {
		return true, false, err.Error()
	}
	return true, true, "explicit nginx.access_log passed static file and parent-directory safety checks; GoAccess runtime readability will be checked after creating system user"
}

func inspectAppRootOnlyFile(field string, path string) (bool, string) {
	info, err := lstatAppServicePathFn(path)
	if err != nil {
		return false, field + " unavailable: " + err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, field + " must not be a symlink"
	}
	if !info.Mode().IsRegular() {
		return false, field + " must be a regular file"
	}
	if info.Size() == 0 {
		return false, field + " must not be empty"
	}
	if info.Mode().Perm()&0o077 != 0 {
		return false, field + " must be root-only, for example mode 0600"
	}
	if uid, ok := fileOwnerUID(info); ok && uid != 0 {
		return false, field + " must be owned by root"
	}
	if err := validateAppRootOnlyFileParents(field, path); err != nil {
		return false, err.Error()
	}
	return true, field + " passed root-only validation"
}

func validateAppRootOnlyFileParents(field string, path string) error {
	return validateAppRootOwnedFileParents(field, path, true)
}

func validateAppRootOwnedFileParents(field string, path string, allowStickyAncestors bool) error {
	dir := filepath.Dir(path)
	immediateParent := dir
	for {
		info, err := lstatAppServicePathFn(dir)
		if err != nil {
			return fmt.Errorf("%s parent directory %s unavailable: %w", field, dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s parent directory %s must not be a symlink", field, dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s parent path %s must be a directory", field, dir)
		}
		if info.Mode().Perm()&0o022 != 0 && (!allowStickyAncestors || dir == immediateParent || info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("%s parent directory %s must not be writable by group or others", field, dir)
		}
		if uid, ok := fileOwnerUID(info); ok && uid != 0 {
			return fmt.Errorf("%s parent directory %s must be owned by root", field, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

type appRuntimeReadTarget struct {
	username string
	uid      uint64
	gids     map[uint64]struct{}
	found    bool
}

func newAppRuntimeReadTarget(username string) appRuntimeReadTarget {
	target := appRuntimeReadTarget{username: strings.TrimSpace(username), gids: map[uint64]struct{}{}}
	if target.username == "" {
		return target
	}
	account, err := user.Lookup(target.username)
	if err != nil {
		return target
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 64)
	if err != nil {
		return target
	}
	target.uid = uid
	target.found = true
	if gid, err := strconv.ParseUint(account.Gid, 10, 64); err == nil {
		target.gids[gid] = struct{}{}
	}
	if groupIDs, err := account.GroupIds(); err == nil {
		for _, groupID := range groupIDs {
			gid, err := strconv.ParseUint(groupID, 10, 64)
			if err == nil {
				target.gids[gid] = struct{}{}
			}
		}
	}
	return target
}

func validateAppRuntimeReadableFile(field string, path string, info os.FileInfo, runtimeLabel string, username string) error {
	target := newAppRuntimeReadTarget(username)
	if !appRuntimeModeAllows(info, target, 0o400, 0o040, 0o004) {
		return fmt.Errorf("%s must be readable by %s runtime user %s", field, runtimeLabel, username)
	}
	dir := filepath.Dir(path)
	for {
		parentInfo, err := lstatAppServicePathFn(dir)
		if err != nil {
			return fmt.Errorf("%s parent directory %s unavailable: %w", field, dir, err)
		}
		if !appRuntimeModeAllows(parentInfo, target, 0o100, 0o010, 0o001) {
			return fmt.Errorf("%s parent directory %s must be searchable by %s runtime user %s", field, dir, runtimeLabel, username)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func appRuntimeModeAllows(info os.FileInfo, target appRuntimeReadTarget, ownerBit fs.FileMode, groupBit fs.FileMode, otherBit fs.FileMode) bool {
	mode := info.Mode().Perm()
	if target.found {
		if uid, ok := fileOwnerUID(info); ok && uid == target.uid && mode&ownerBit != 0 {
			return true
		}
		if gid, ok := fileOwnerGID(info); ok {
			if _, groupMember := target.gids[gid]; groupMember && mode&groupBit != 0 {
				return true
			}
		}
	}
	return mode&otherBit != 0
}

func detectAppServiceBinary(cfg appconfig.Config) (bool, string) {
	if cfg.Mode() != appconfig.ModeListen {
		return true, ""
	}
	path := cfg.ServiceBinary()
	if path == "" {
		return false, ""
	}
	info, err := statAppServiceBinaryFn(path)
	if err != nil || info.IsDir() {
		return false, path
	}
	return info.Mode().Perm()&0o111 != 0, path
}

func guardAppOwnership(fileSystem host.FileSystem, cfg appconfig.Config, staged []apprender.StagedFile) error {
	for _, file := range staged {
		info, err := fileSystem.Lstat(file.HostPath)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat existing %s: %w", file.HostPath, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to inspect managed app file", file.HostPath)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s exists and is not a regular file; refusing to inspect managed app file", file.HostPath)
		}
		content, err := fileSystem.ReadFile(file.HostPath)
		if err != nil {
			return fmt.Errorf("read existing %s: %w", file.HostPath, err)
		}
		if err := appsvc.CheckManagedContent(cfg.App.Name, content); err != nil {
			return fmt.Errorf("%s: %w", file.HostPath, err)
		}
	}
	return nil
}

func convertAppStagedFiles(files []apprender.StagedFile) []render.StagedFile {
	converted := make([]render.StagedFile, 0, len(files))
	for _, file := range files {
		converted = append(converted, render.StagedFile{
			SourcePath:  file.SourcePath,
			HostPath:    file.HostPath,
			ContentMode: file.ContentMode,
			Mode:        file.Mode,
			Content:     file.Content,
		})
	}
	return converted
}

func ensureAppHostDependencies(ctx stdcontext.Context, cfg appconfig.Config, executor host.Executor) error {
	if _, err := executor.AptGet(ctx, "update"); err != nil {
		return err
	}
	installArgs := append([]string{"install", "-y"}, appHostDependencyPackages(cfg)...)
	if _, err := executor.AptGet(ctx, installArgs...); err != nil {
		return err
	}
	if cfg.Nginx.GoAccess.Enabled {
		if err := ensureAppGoAccessDependency(ctx, executor); err != nil {
			return err
		}
	}
	legoResult, err := executor.Run(ctx, host.Command{Name: legocomponent.BinaryPath, Args: []string{"--version"}})
	if err == nil {
		return nil
	}
	if !host.CommandMissing(legoResult, err, legocomponent.BinaryPath, "lego") {
		return err
	}
	legoCfg := config.ExampleConfig()
	detected, detectErr := executor.Dpkg(ctx, "--print-architecture")
	if detectErr != nil {
		return fmt.Errorf("detect package architecture with dpkg --print-architecture: %w", detectErr)
	}
	switch strings.TrimSpace(detected.Stdout) {
	case "amd64":
		legoCfg.Advanced.Platform.Arch = config.ArchAMD64
	case "arm64":
		legoCfg.Advanced.Platform.Arch = config.ArchARM64
	default:
		return fmt.Errorf("unsupported package architecture %q for app lego install", strings.TrimSpace(detected.Stdout))
	}
	plan, err := legocomponent.NewInstallPlan(legoCfg, legocomponent.InstallPlanOptions{})
	if err != nil {
		return err
	}
	_, err = legocomponent.NewInstaller(executor).Install(ctx, plan)
	return err
}

func ensureAppNginxRuntimeCompatibility(ctx stdcontext.Context, cfg appconfig.Config, executor host.Executor) error {
	needsHTTP2 := cfg.NginxHTTP2Enabled()
	needsGzipStatic := appNginxUsesGzipStatic(cfg)
	needsRealIP := cfg.RealIPEnabled()
	if !needsHTTP2 && !needsGzipStatic && !needsRealIP {
		return nil
	}
	result, err := executor.Run(ctx, host.Command{Name: appsvc.NginxBinaryPath, Args: []string{"-V"}})
	if err != nil {
		return fmt.Errorf("run %s -V to confirm Nginx module support: %w", appsvc.NginxBinaryPath, err)
	}
	output := result.Stdout + "\n" + result.Stderr
	if needsHTTP2 {
		version, ok := parseNginxVersion(output)
		if !ok {
			return fmt.Errorf("unable to parse nginx -V output while confirming HTTP/2 directive support")
		}
		if compareDottedVersion(version, minimumNginxHTTP2DirectiveVersion) < 0 {
			return fmt.Errorf("nginx %s does not support the modern \"http2 on;\" directive; require nginx >= %s or set nginx.http2: false", version, minimumNginxHTTP2DirectiveVersion)
		}
		if !strings.Contains(output, "--with-http_v2_module") {
			return fmt.Errorf("installed Nginx was not built with --with-http_v2_module; set nginx.http2: false or install an Nginx package with HTTP/2 support")
		}
	}
	if needsGzipStatic && !strings.Contains(output, "--with-http_gzip_static_module") {
		return fmt.Errorf("installed Nginx was not built with --with-http_gzip_static_module; remove gzip_static: true or install an Nginx package with gzip_static support")
	}
	if needsRealIP && !strings.Contains(output, "--with-http_realip_module") {
		return fmt.Errorf("installed Nginx was not built with --with-http_realip_module; install an Nginx package with ngx_http_realip_module before enabling nginx.realip_profile")
	}
	return nil
}

func appNginxUsesGzipStatic(cfg appconfig.Config) bool {
	for _, location := range cfg.Nginx.StaticLocations {
		if location.GzipStatic {
			return true
		}
	}
	return false
}

func parseNginxVersion(output string) (string, bool) {
	match := nginxVersionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return "", false
	}
	return match[1] + "." + match[2] + "." + match[3], true
}

func compareDottedVersion(left string, right string) int {
	leftParts := parseDottedVersion(left)
	rightParts := parseDottedVersion(right)
	for index := 0; index < 3; index++ {
		switch {
		case leftParts[index] < rightParts[index]:
			return -1
		case leftParts[index] > rightParts[index]:
			return 1
		}
	}
	return 0
}

func parseDottedVersion(value string) [3]int {
	parts := strings.Split(value, ".")
	var parsed [3]int
	for index := 0; index < len(parsed) && index < len(parts); index++ {
		number, err := strconv.Atoi(parts[index])
		if err != nil {
			continue
		}
		parsed[index] = number
	}
	return parsed
}

func ensureAppGoAccessDependency(ctx stdcontext.Context, executor host.Executor) error {
	versionResult, err := executor.Run(ctx, goAccessProbeCommand("--version"))
	if err != nil {
		return fmt.Errorf("%s --version failed after package install: %w", appsvc.GoAccessBinaryPath, commandErrorWithOutput(versionResult, err))
	}
	result, err := executor.Run(ctx, goAccessProbeCommand("--help"))
	help := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if err != nil && !goAccessHelpHasAllRequiredOptions(help) {
		return fmt.Errorf("%s --help failed while checking required runtime parameters: %w", appsvc.GoAccessBinaryPath, commandErrorWithOutput(result, err))
	}
	if help == "" {
		return fmt.Errorf("%s --help returned empty output while checking required runtime parameters", appsvc.GoAccessBinaryPath)
	}
	for _, flag := range goAccessRequiredRuntimeOptions() {
		if !goAccessHelpHasOption(help, flag) {
			return fmt.Errorf("installed %s does not advertise required option %s; install a newer GoAccess package", appsvc.GoAccessBinaryPath, flag)
		}
	}
	result, err = executor.Run(ctx, goAccessFreshDBCompatibilityCommand())
	if err != nil {
		return fmt.Errorf("installed %s failed Lanpanel fresh db persist/restore compatibility check: %w", appsvc.GoAccessBinaryPath, commandErrorWithOutput(result, err))
	}
	return nil
}

func goAccessProbeCommand(arg string) host.Command {
	return host.Command{
		Name: appsvc.GoAccessBinaryPath,
		Args: []string{arg},
		Env:  goAccessProbeEnv(),
	}
}

func goAccessProbeEnv() map[string]string {
	return map[string]string{
		"LANG":   "C",
		"LC_ALL": "C",
	}
}

func goAccessFreshDBCompatibilityCommand() host.Command {
	script := `set -eu
binary=$1
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/db"
cat > "$work/access.log" <<'LOG'
203.0.113.10 - - [2026-05-24T21:00:00+08:00] "GET /lanpanel-goaccess-probe HTTP/1.1" 200 123 "-" "lanpanel-goaccess-probe" "lanpanel.invalid" 0.001 "-" "-"
LOG
cat > "$work/goaccess.conf" <<EOF
log-file $work/access.log
output $work/report.html
log-format %h %^ %^ [%x] "%r" %s %b "%R" "%u" "%v" %T "%^" "%^"
datetime-format %Y-%m-%dT%H:%M:%S%z
persist true
restore true
db-path $work/db
html-report-title Lanpanel-GoAccess-Probe
EOF
if ! "$binary" --no-global-config --config-file "$work/goaccess.conf" >"$work/stdout" 2>"$work/stderr"; then
    cat "$work/stdout" >&2
    cat "$work/stderr" >&2
    exit 1
fi
if [ ! -s "$work/report.html" ]; then
    echo "GoAccess fresh db compatibility probe did not create an HTML report" >&2
    exit 1
fi
db_file=$(find "$work/db" -maxdepth 1 -type f -name '*.db' -print -quit)
if [ -z "$db_file" ]; then
    echo "GoAccess fresh db compatibility probe did not create persisted db files" >&2
    exit 1
fi`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "lanpanel-app-goaccess-fresh-db-compatibility", appsvc.GoAccessBinaryPath},
		Env:         goAccessProbeEnv(),
		DisplayName: "check-goaccess-fresh-db-compatibility",
		DisplayArgs: []string{appsvc.GoAccessBinaryPath},
	}
}

func goAccessHelpHasOption(help string, option string) bool {
	for _, field := range strings.Fields(help) {
		token := strings.Trim(field, " ,;")
		if token == option || strings.HasPrefix(token, option+"=") {
			return true
		}
	}
	return false
}

func goAccessHelpHasAllRequiredOptions(help string) bool {
	if strings.TrimSpace(help) == "" {
		return false
	}
	for _, flag := range goAccessRequiredRuntimeOptions() {
		if !goAccessHelpHasOption(help, flag) {
			return false
		}
	}
	return true
}

func goAccessRequiredRuntimeOptions() []string {
	return []string{"--no-global-config", "--config-file", "--log-file", "--output", "--log-format", "--datetime-format", "--date-format", "--time-format", "--real-time-html", "--addr", "--port", "--ws-url", "--origin", "--ping-interval", "--persist", "--restore", "--db-path", "--html-report-title", "--static-file"}
}

func appHostDependencyPackages(cfg appconfig.Config) []string {
	packages := []string{"nginx", "ca-certificates", "curl", "tar", "openssl"}
	if cfg.Nginx.GoAccess.Enabled {
		packages = append(packages, "goaccess")
		if appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
			packages = append(packages, "logrotate")
		}
	}
	return packages
}

func ensureAppTailscale(ctx stdcontext.Context, cfg appconfig.Config, executor host.Executor) (tailscalecomponent.EnsureResult, error) {
	loginServer, err := appTailscaleLoginServer(cfg)
	if err != nil {
		return tailscalecomponent.EnsureResult{}, err
	}
	client := tailscalecomponent.NewClient(executor, detectPlatformInfoFn())
	authKey := ""
	result, err := client.Ensure(ctx, tailscalecomponent.EnsurePlan{
		Required:    cfg.RequiresTailscale(),
		LoginServer: loginServer,
		Hostname:    cfg.Tailscale.Hostname,
		AuthKeyFunc: func(ctx stdcontext.Context) (string, error) {
			var err error
			authKey, err = appTailscaleAuthKey(ctx, cfg, executor)
			return authKey, err
		},
	})
	if err == nil {
		return result, nil
	}
	if authKey != "" {
		return result, fmt.Errorf("%s", tailscalecomponent.MaskText(err.Error(), authKey))
	}
	return result, err
}

func appTailscaleLoginServer(cfg appconfig.Config) (string, error) {
	if strings.TrimSpace(cfg.Tailscale.LoginServer) != "" {
		return strings.TrimSpace(cfg.Tailscale.LoginServer), nil
	}
	mainConfigPath := cfg.EffectiveLanpanelConfig()
	mainCfg, err := config.LoadFile(mainConfigPath)
	if err != nil {
		return "", fmt.Errorf("load tailscale.lanpanel_config %s: %w", mainConfigPath, err)
	}
	return mainCfg.Default.ServerURL, nil
}

func validateAppAgainstMainConfig(cfg appconfig.Config) error {
	mainConfigPath := cfg.EffectiveLanpanelConfig()
	mainConfigRequired := cfg.RequiresTailscale() && strings.TrimSpace(cfg.Tailscale.LoginServer) == ""
	if strings.TrimSpace(mainConfigPath) == "" {
		mainConfigPath = appconfig.DefaultLanpanelConfigPath
	}
	mainCfg, err := config.LoadFile(mainConfigPath)
	if err != nil {
		if mainConfigRequired {
			return fmt.Errorf("load tailscale.lanpanel_config %s: %w", mainConfigPath, err)
		}
		return nil
	}
	if err := validateAppDoesNotReuseMainServerDomain(cfg, mainCfg, mainConfigPath); err != nil {
		return err
	}
	if err := validateAppDoesNotReuseHeadscaleMetricsPort(cfg, mainCfg, mainConfigPath); err != nil {
		return err
	}
	return nil
}

func validateAppDoesNotReuseMainServerDomain(cfg appconfig.Config, mainCfg config.Config, mainConfigPath string) error {
	serverHost, err := serverURLHost(mainCfg.Default.ServerURL)
	if err != nil {
		return fmt.Errorf("parse Headscale server_url from %s: %w", mainConfigPath, err)
	}
	for _, domain := range cfg.App.Domains {
		if normalizeDomainForCompare(domain) == serverHost {
			return fmt.Errorf("app.domains must not reuse Headscale server_url host %q from %s", serverHost, mainConfigPath)
		}
	}
	return nil
}

func validateAppDoesNotReuseHeadscaleMetricsPort(cfg appconfig.Config, mainCfg config.Config, mainConfigPath string) error {
	const headscaleMetricsBindHost = "127.0.0.1"

	metricsPort := mainCfg.Advanced.Headscale.MetricsPort
	if metricsPort <= 0 {
		return nil
	}
	if cfg.Mode() == appconfig.ModeListen {
		host, port, ok := splitAppHostPort(cfg.App.Listen)
		if ok && port == metricsPort && socketBindHostsOverlap(host, headscaleMetricsBindHost) {
			return fmt.Errorf("app.listen must not reuse Headscale metrics port %d from %s", metricsPort, mainConfigPath)
		}
	}
	if cfg.Nginx.GoAccess.Enabled {
		listen := appconfig.EffectiveNginxGoAccessWebSocketListen(cfg.App.Name, cfg.Nginx.GoAccess)
		host, port, ok := splitAppHostPort(listen)
		if ok && port == metricsPort && socketBindHostsOverlap(host, headscaleMetricsBindHost) {
			return fmt.Errorf("nginx.goaccess.websocket_listen must not reuse Headscale metrics port %d from %s", metricsPort, mainConfigPath)
		}
	}
	return nil
}

func splitAppHostPort(listen string) (string, int, bool) {
	host, portString, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, false
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		return "", 0, false
	}
	return host, port, true
}

func appMainConfigConflictResponse(configPath string, command string, err error) output.Response {
	return output.Response{
		Command: command,
		Status:  "invalid-config",
		Summary: "App config conflicts with main Headscale server_url or metrics_port",
		Fields: []output.Field{
			{Label: "config path", Value: configPath},
			{Label: "details", Value: err.Error()},
		},
		NextSteps: []string{"Change app.domains, app.listen, or nginx.goaccess.websocket_listen so the app uses separate domains and does not reuse the main Headscale port."},
	}
}

func serverURLHost(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	host := normalizeDomainForCompare(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("server_url host is required")
	}
	return host, nil
}

func normalizeDomainForCompare(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func appTailscaleAuthKey(ctx stdcontext.Context, cfg appconfig.Config, executor host.Executor) (string, error) {
	if cfg.Tailscale.AuthKeyFile != "" {
		key, err := tailscalecomponent.ReadAuthKeyFile(cfg.Tailscale.AuthKeyFile)
		if err != nil {
			return "", fmt.Errorf("tailscale.auth_key_file unavailable: %w", err)
		}
		return key, nil
	}
	if cfg.Tailscale.LoginServer != "" {
		return "", fmt.Errorf("external tailscale.login_server requires an already-logged-in client or tailscale.auth_key_file")
	}
	plan, err := headscale.NewOnboardingPlan(headscale.OnboardingOptions{
		Expiration: time.Hour,
	})
	if err != nil {
		return "", err
	}
	key, _, err := headscale.NewOnboarding(executor).CreatePreAuthKey(ctx, plan)
	return key, err
}

func removeStaleAppServiceUnit(ctx stdcontext.Context, executor host.Executor, names appsvc.Names) ([]string, error) {
	result, err := executor.Run(ctx, appsvc.RemoveManagedServiceUnitCommand(names))
	if err != nil {
		return nil, err
	}
	return outputLines(result.Stdout), nil
}

func removeStaleGoAccessRuntime(ctx stdcontext.Context, executor host.Executor, names appsvc.Names) ([]string, error) {
	result, err := executor.Run(ctx, appsvc.RemoveManagedGoAccessRuntimeCommand(names))
	if err != nil {
		return nil, err
	}
	return outputLines(result.Stdout), nil
}

func removeStaleGoAccessLogrotate(ctx stdcontext.Context, executor host.Executor, names appsvc.Names) ([]string, error) {
	result, err := executor.Run(ctx, appsvc.RemoveManagedGoAccessLogrotateCommand(names))
	if err != nil {
		return nil, err
	}
	return outputLines(result.Stdout), nil
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func activateAppNginx(ctx stdcontext.Context, executor host.Executor, names appsvc.Names) error {
	return activateAppNginxTracking(ctx, executor, names, nil)
}

func activateAppNginxTracking(ctx stdcontext.Context, executor host.Executor, names appsvc.Names, effects *appDeployEffects) error {
	if err := prepareAppNginxActivation(ctx, executor, names, effects); err != nil {
		return err
	}
	return reloadAppNginxTracking(ctx, executor, effects)
}

func prepareAppNginxActivation(ctx stdcontext.Context, executor host.Executor, names appsvc.Names, effects *appDeployEffects) error {
	for _, command := range []host.Command{appsvc.GuardEnabledSiteCommand(names), appsvc.EnableSiteCommand(names), appsvc.TestNginxCommand()} {
		if _, err := executor.Run(ctx, command); err != nil {
			return err
		}
		if effects == nil {
			continue
		}
		switch command.Name {
		case "ln":
			effects.AddPaths(names.NginxEnabledPath)
			effects.AddActions("enabled app Nginx site")
		case appsvc.NginxBinaryPath:
			effects.AddActions("tested Nginx config")
		}
	}
	return nil
}

func reloadAppNginxTracking(ctx stdcontext.Context, executor host.Executor, effects *appDeployEffects) error {
	if _, err := executor.Run(ctx, appsvc.ReloadNginxCommand()); err != nil {
		return err
	}
	if effects != nil {
		effects.AddActions("reloaded Nginx")
	}
	return nil
}

func appDeployFailureWithEffects(formatter output.Formatter, summary string, err error, effects appDeployEffects) error {
	return appDeployFailureWithFields(formatter, summary, err, effects.Fields())
}

func appDeployFailureWithFields(formatter output.Formatter, summary string, err error, fields []output.Field) error {
	responseFields := []output.Field{{Label: "details", Value: err.Error()}}
	responseFields = append(responseFields, fields...)
	response := output.Response{
		Command:   "app deploy",
		Status:    "failed",
		Summary:   summary,
		Fields:    responseFields,
		NextSteps: []string{"Fix the error and rerun the same lanpanel app deploy command."},
	}
	if writeErr := formatter.Write(response); writeErr != nil {
		return writeErr
	}
	return fmt.Errorf("app deploy: %s: %w", summary, err)
}

func outputLines(text string) []string {
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func writeAppFailureResponse(formatter output.Formatter, response output.Response) error {
	if err := formatter.Write(response); err != nil {
		return err
	}
	if strings.TrimSpace(response.Summary) == "" {
		return fmt.Errorf("%s failed", response.Command)
	}
	return fmt.Errorf("%s: %s", response.Command, response.Summary)
}

func writeAppHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"lanpanel app manages additional Go services and tailnet upstreams.",
		"",
		"Usage:",
		"  lanpanel app <command> [flags]",
		"",
		"Commands:",
		"  init    Generate an editable app example config.",
		"  deploy  Deploy an app from config.",
		"  realip  Manage app real client IP profile runtime artifacts.",
		"  verify  Validate app config and runtime templates.",
	)
}

func writeAppInitHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Generate an editable app example config.",
		"",
		"Usage:",
		"  lanpanel app init [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the lanpanel app config file to create.",
		"  --format string   Output format: human | json",
	)
}

func writeAppVerifyHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Validate app config and runtime templates.",
		"",
		"Usage:",
		"  lanpanel app verify [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the lanpanel app config file.",
		"  --format string   Output format: human | json",
	)
}

func writeAppDeployHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Deploy an app from config.",
		"",
		"Usage:",
		"  lanpanel app deploy [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the lanpanel app config file.",
		"  --format string   Output format: human | json",
	)
}

func writeAppRealIPHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Manage app real client IP profile runtime artifacts.",
		"",
		"Usage:",
		"  lanpanel app realip <command> [flags]",
		"",
		"Commands:",
		"  diagnostics         Report deployed realip profile diagnostics.",
		"  refresh             Refresh a deployed realip profile from EdgeOne OriginACL.",
		"  validate-reference  Validate a deployed realip app reference.",
	)
}

func writeAppRealIPDiagnosticsHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Report deployed realip profile diagnostics.",
		"",
		"Usage:",
		"  lanpanel app realip diagnostics --profile name [--format human|json]",
		"",
		"Flags:",
		"  --profile string  Name of the deployed realip profile to diagnose.",
		"  --format string   Output format: human | json",
	)
}

func writeAppRealIPRefreshHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Refresh a deployed realip profile from EdgeOne OriginACL.",
		"",
		"Usage:",
		"  lanpanel app realip refresh --profile name [--format human|json]",
		"",
		"Flags:",
		"  --profile string  Name of the deployed realip profile to refresh.",
		"  --format string   Output format: human | json",
	)
}

func writeAppRealIPValidateReferenceHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Validate a deployed realip app reference.",
		"",
		"Usage:",
		"  lanpanel app realip validate-reference --profile name --app name --path path",
		"",
		"Flags:",
		"  --profile string  Expected deployed realip profile name.",
		"  --app string      Expected app name from the reference filename.",
		"  --path string     Path to the deployed realip reference JSON.",
	)
}
