package cli

import (
	stdcontext "context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"meshify/internal/acme"
	"meshify/internal/appconfig"
	"meshify/internal/apppreflight"
	"meshify/internal/apprender"
	"meshify/internal/appverify"
	"meshify/internal/components/appsvc"
	"meshify/internal/components/headscale"
	legocomponent "meshify/internal/components/lego"
	nginxcomponent "meshify/internal/components/nginx"
	tailscalecomponent "meshify/internal/components/tailscale"
	"meshify/internal/config"
	"meshify/internal/host"
	"meshify/internal/output"
	"meshify/internal/preflight"
	"meshify/internal/render"
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
	"time"
	"unicode"
)

const DefaultAppConfigPath = appconfig.DefaultConfigPath

const minimumNginxHTTP2DirectiveVersion = "1.25.1"

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
			effects.AddPaths("/usr/share/keyrings/tailscale-archive-keyring.gpg", "/usr/share/keyrings/tailscale-archive-keyring.gpg.meshify-managed")
		case strings.Contains(command, "install-tailscale-apt-source"):
			effects.AddPaths("/etc/apt/sources.list.d/tailscale.list", "/etc/apt/sources.list.d/tailscale.list.meshify-managed")
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
	newAppFileInstallerFn                = func(executor host.Executor, privilege host.PrivilegeStrategy) appStagedFileInstaller {
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
	selected, ok := appCommands[args[0]]
	if !ok {
		if err := writeAppHelp(ctx.stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown app command %q", args[0])
	}
	return selected.usage(ctx.stdout)
}

func (options *appOptions) bind(flagSet interface {
	StringVar(*string, string, string, string)
}) {
	flagSet.StringVar(&options.configPath, "config", DefaultAppConfigPath, "Path to the meshify app config file.")
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
			fmt.Sprintf("Run 'sudo meshify app deploy --config %s' to deploy the app.", options.configPath),
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
			fmt.Sprintf("Run 'sudo meshify app deploy --config %s' to apply or refresh app runtime files.", options.configPath),
			appRuntimeHostChecksStep(cfg),
		},
	}
	if report.FailedCount() > 0 {
		return writeAppFailureResponse(formatter, response)
	}
	return formatter.Write(response)
}

func runAppDeploy(ctx context, args []string) error {
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
			NextSteps: []string{"Run 'meshify app verify' first, then fix the failed static checks."},
		})
	}

	executor := newHostExecutorFn(nil)
	privilege := deployPrivilegeStrategy(permissions)
	privilegedExecutor := executor.WithPrivilege(privilege)
	fileSystem := newAppHostFileSystemFn(privilegedExecutor, privilege)
	systemd := newHostSystemdFn(privilegedExecutor)
	if err := guardAppOwnership(fileSystem, cfg, staged); err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app deploy",
			Status:    "blocked",
			Summary:   "Target file exists and is not a Meshify-managed file for this app",
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

	results, err := newAppFileInstallerFn(privilegedExecutor, privilege).Install(convertAppStagedFiles(staged))
	effects.AddPaths(host.CollectModifiedPaths(results)...)
	if err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to write app runtime files", err, effects)
	}
	if cfg.Nginx.GoAccess.Enabled {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardGoAccessRuntimeAccessCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "GoAccess runtime permission check failed", err, effects)
		}
		effects.AddActions("checked GoAccess runtime permissions")
	}
	if _, err := systemd.DaemonReload(stdcontext.Background()); err != nil {
		return appDeployFailureWithEffects(formatter, "systemd daemon-reload failed", err, effects)
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
		if err := prepareAppNginxActivation(stdcontext.Background(), privilegedExecutor, names, &effects); err != nil {
			return "Failed to enable app Nginx site", err
		}
		if summary, err := clearGoAccessAppListenBlockersBeforeNginxReload(); err != nil {
			return summary, err
		}
		if summary, err := clearGoAccessAppPortBlockersBeforeNginxReload(); err != nil {
			return summary, err
		}
		if err := reloadAppNginxTracking(stdcontext.Background(), privilegedExecutor, &effects); err != nil {
			return "Failed to enable app Nginx site", err
		}
		return "", nil
	}

	if cfg.App.ACMEChallenge == appconfig.ACMEChallengeHTTP01 {
		for _, command := range appsvc.HTTP01BootstrapCommands(names) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return appDeployFailureWithEffects(formatter, "Failed to prepare HTTP-01 bootstrap certificate", err, effects)
			}
		}
		effects.AddPaths(names.WebrootPath, names.LegoDataPath, names.TLSDir, names.FullchainPath, names.PrivateKeyPath)
		effects.AddActions("prepared HTTP-01 bootstrap certificate")
		if summary, err := activateAppNginxBeforeReload(); err != nil {
			return appDeployFailureWithEffects(formatter, summary, err, effects)
		}
		if summary, err := cleanupStaleGoAccessPostCutover(); err != nil {
			return appDeployFailureWithEffects(formatter, summary, err, effects)
		}
	}
	certPlan, err := appsvc.NewCertificatePlan(cfg, names)
	if err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to build app TLS certificate plan", err, effects)
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), legocomponent.MigrationGateCommand(names.LegoDataPath)); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to migrate app lego v5 storage", err, effects)
	}
	if _, err := runHostCommandWithProgress(
		stdcontext.Background(),
		privilegedExecutor,
		certPlan.Command,
		ctx.stdout,
		format,
		dns01CertificateProgress("app deploy", cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01),
	); err != nil {
		return appDeployFailureWithEffects(formatter, "Failed to issue app TLS certificate", err, effects)
	}
	effects.AddPaths(names.LegoDataPath, names.FullchainPath, names.PrivateKeyPath)
	effects.AddActions("issued or renewed app certificate")
	if cfg.App.ACMEChallenge != appconfig.ACMEChallengeHTTP01 {
		if summary, err := activateAppNginxBeforeReload(); err != nil {
			return appDeployFailureWithEffects(formatter, summary, err, effects)
		}
		if summary, err := cleanupStaleGoAccessPostCutover(); err != nil {
			return appDeployFailureWithEffects(formatter, summary, err, effects)
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

	fields := append(appConfigFields(options.configPath, cfg),
		output.Field{Label: "nginx site", Value: names.NginxAvailablePath},
		output.Field{Label: "renew timer", Value: names.RenewTimerUnit},
	)
	fields = append(fields, appGoAccessDeployFields(cfg, names)...)
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
		NextSteps: []string{"Rerun with sudo meshify app deploy."},
	}
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
					fmt.Sprintf("Run 'meshify app init --config %s' to generate an example config.", path),
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
	return fmt.Sprintf("Run 'meshify app verify --config %s' to recheck static config and templates; then use %s to verify host runtime state.", configPath, appRuntimeHostCheckTools(cfg))
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
	mainConfigPath := cfg.EffectiveMeshifyConfig()
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
	request.Header.Set("User-Agent", "meshify-app-preflight/1.0")
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
		return true, false, "Detected Route53 AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY in the current environment, but meshify app deploy will not pass raw AWS secrets through sudo or systemd. Use dns01.env_file for DNS-01 deploy and renewal."
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
	return isMeshifyManagedSystemdUnitPortBinding(names.AppName, names.GoAccessServiceUnit, binding)
}

func isAppManagedPortBinding(names appsvc.Names, binding preflight.PortBinding) (bool, bool) {
	if strings.TrimSpace(binding.Process) == "" || binding.PID <= 0 {
		return false, false
	}
	return isMeshifyManagedSystemdUnitPortBinding(names.AppName, names.ServiceUnit, binding)
}

func isMeshifyManagedSystemdUnitPortBinding(appName string, unit string, binding preflight.PortBinding) (bool, bool) {
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
	if !needsHTTP2 && !needsGzipStatic {
		return nil
	}
	result, err := executor.Run(ctx, host.Command{Name: "nginx", Args: []string{"-V"}})
	if err != nil {
		return fmt.Errorf("run nginx -V to confirm Nginx module support: %w", err)
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
	if _, err := executor.Run(ctx, host.Command{Name: appsvc.GoAccessBinaryPath, Args: []string{"--version"}}); err != nil {
		return fmt.Errorf("%s --version failed after package install: %w", appsvc.GoAccessBinaryPath, err)
	}
	result, err := executor.Run(ctx, host.Command{Name: appsvc.GoAccessBinaryPath, Args: []string{"--help"}})
	if err != nil {
		return fmt.Errorf("%s --help failed while checking required runtime parameters: %w", appsvc.GoAccessBinaryPath, err)
	}
	help := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if help == "" {
		return fmt.Errorf("%s --help returned empty output while checking required runtime parameters", appsvc.GoAccessBinaryPath)
	}
	for _, flag := range []string{"--no-global-config", "--config-file", "--log-file", "--output", "--log-format", "--datetime-format", "--date-format", "--time-format", "--real-time-html", "--addr", "--port", "--ws-url", "--origin", "--ping-interval", "--persist", "--restore", "--db-path", "--html-report-title", "--static-file"} {
		if !goAccessHelpHasOption(help, flag) {
			return fmt.Errorf("installed %s does not advertise required option %s; install a newer GoAccess package", appsvc.GoAccessBinaryPath, flag)
		}
	}
	if _, err := executor.Run(ctx, goAccessFreshDBCompatibilityCommand()); err != nil {
		return fmt.Errorf("installed %s failed Meshify fresh db persist/restore compatibility check: %w", appsvc.GoAccessBinaryPath, err)
	}
	return nil
}

func goAccessFreshDBCompatibilityCommand() host.Command {
	script := `set -eu
binary=$1
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/db"
cat > "$work/access.log" <<'LOG'
203.0.113.10 - - [2026-05-24T21:00:00+08:00] "GET /meshify-goaccess-probe HTTP/1.1" 200 123 "-" "meshify-goaccess-probe" "meshify.invalid" 0.001 "-" "-"
LOG
cat > "$work/goaccess.conf" <<EOF
log-file $work/access.log
output $work/report.html
log-format %h %^ %^ [%x] "%r" %s %b "%R" "%u" "%v" %T "%^" "%^"
datetime-format %Y-%m-%dT%H:%M:%S%z
persist true
restore true
db-path $work/db
html-report-title Meshify-GoAccess-Probe
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
		Args:        []string{"-c", script, "meshify-app-goaccess-fresh-db-compatibility", appsvc.GoAccessBinaryPath},
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
	mainConfigPath := cfg.EffectiveMeshifyConfig()
	mainCfg, err := config.LoadFile(mainConfigPath)
	if err != nil {
		return "", fmt.Errorf("load tailscale.meshify_config %s: %w", mainConfigPath, err)
	}
	return mainCfg.Default.ServerURL, nil
}

func validateAppAgainstMainConfig(cfg appconfig.Config) error {
	mainConfigPath := cfg.EffectiveMeshifyConfig()
	mainConfigRequired := cfg.RequiresTailscale() && strings.TrimSpace(cfg.Tailscale.LoginServer) == ""
	if strings.TrimSpace(mainConfigPath) == "" {
		mainConfigPath = appconfig.DefaultMeshifyConfigPath
	}
	mainCfg, err := config.LoadFile(mainConfigPath)
	if err != nil {
		if mainConfigRequired {
			return fmt.Errorf("load tailscale.meshify_config %s: %w", mainConfigPath, err)
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
		case "nginx":
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
		NextSteps: []string{"Fix the error and rerun the same meshify app deploy command."},
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
		"meshify app manages additional Go services and tailnet upstreams.",
		"",
		"Usage:",
		"  meshify app <command> [flags]",
		"",
		"Commands:",
		"  init    Generate an editable app example config.",
		"  deploy  Deploy an app from config.",
		"  verify  Validate app config and runtime templates.",
	)
}

func writeAppInitHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Generate an editable app example config.",
		"",
		"Usage:",
		"  meshify app init [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the meshify app config file to create.",
		"  --format string   Output format: human | json",
	)
}

func writeAppVerifyHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Validate app config and runtime templates.",
		"",
		"Usage:",
		"  meshify app verify [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the meshify app config file.",
		"  --format string   Output format: human | json",
	)
}

func writeAppDeployHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Deploy an app from config.",
		"",
		"Usage:",
		"  meshify app deploy [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the meshify app config file.",
		"  --format string   Output format: human | json",
	)
}
