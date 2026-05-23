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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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
		"deploy": {summary: "按 app 配置部署同机服务或 tailnet upstream。", usage: writeAppDeployHelp, run: runAppDeploy},
		"init":   {summary: "生成可编辑的 app 示例配置。", usage: writeAppInitHelp, run: runAppInit},
		"verify": {summary: "校验 app 配置和 runtime 模板。", usage: writeAppVerifyHelp, run: runAppVerify},
	}

	stageAppRuntimeFilesFn               = apprender.StageRuntime
	statAppServiceBinaryFn               = os.Stat
	lstatAppServicePathFn                = os.Lstat
	detectAppDNSFn                       = detectAppDNS
	detectAppCurrentPublicIPsFn          = detectAppCurrentPublicIPs
	detectAppPortBindingsFn              = detectAppPortBindings
	detectAppDNSCredentialStateFn        = detectAppDNSCredentialState
	detectAppServiceEnvFileStateFn       = detectAppServiceEnvFileState
	detectAppTailscaleAuthKeyFileStateFn = detectAppTailscaleAuthKeyFileState
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
		summary: "管理附加 app 部署。",
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
	flagSet.StringVar(&options.configPath, "config", DefaultAppConfigPath, "meshify app 配置文件路径。")
	flagSet.StringVar(&options.formatValue, "format", string(output.FormatHuman), "输出格式：human | json")
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
		Summary: "已写入 app 示例配置",
		Fields:  []output.Field{{Label: "config path", Value: options.configPath}},
		NextSteps: []string{
			"编辑 app.domains、listen 或 upstream，以及 service 配置。",
			fmt.Sprintf("运行 'sudo meshify app deploy --config %s' 部署 app。", options.configPath),
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
			Summary:   "app runtime 模板渲染失败",
			Fields:    []output.Field{{Label: "config path", Value: options.configPath}, {Label: "details", Value: err.Error()}},
			NextSteps: []string{"修正 app 配置或模板输入后重新运行 verify。"},
		})
	}
	report := appverify.StaticReport(cfg, staged)
	status := "static-passed"
	if report.FailedCount() > 0 {
		status = "failed"
	}
	fields := appConfigFields(options.configPath, cfg)
	fields = append(fields, output.Field{Label: "verification scope", Value: "static-only: 未连接宿主机，不校验已部署文件、systemd、证书、Nginx runtime 或 Tailscale 在线状态"})
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
			fmt.Sprintf("运行 'sudo meshify app deploy --config %s' 应用或刷新 app runtime 文件。", options.configPath),
			"已部署后使用 nginx -t、systemctl、证书检查、curl 和 tailscale status 验证宿主机状态。",
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
	dnsCredentialsChecked, dnsCredentialsReady, dnsCredentialsDetail := detectAppDNSCredentialStateFn(cfg)
	serviceEnvFileChecked, serviceEnvFileReady, serviceEnvFileDetail := detectAppServiceEnvFileStateFn(cfg)
	authKeyFileChecked, authKeyFileReady, authKeyFileDetail := detectAppTailscaleAuthKeyFileStateFn(cfg)
	preflightReport := apppreflight.BuildReport(cfg, apppreflight.Inputs{
		Permissions:                 permissions,
		DNS:                         dns,
		Ports:                       detectAppPortBindingsFn(),
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
			Summary: "app runtime 模板渲染失败",
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
			NextSteps: []string{"先运行 'meshify app verify' 查看并修复静态检查失败项。"},
		})
	}

	executor := newHostExecutorFn(nil)
	privilege := deployPrivilegeStrategy(permissions)
	privilegedExecutor := executor.WithPrivilege(privilege)
	fileSystem := newAppHostFileSystemFn(privilegedExecutor, privilege)
	if err := guardAppOwnership(fileSystem, cfg, staged); err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app deploy",
			Status:    "blocked",
			Summary:   "目标文件存在且不属于当前 app 的 Meshify-managed 文件",
			Fields:    []output.Field{{Label: "details", Value: err.Error()}},
			NextSteps: []string{"检查冲突文件，确认后手动迁移、删除，或更换 app.name。"},
		})
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardEnabledSiteCommand(names)); err != nil {
		return writeAppFailureResponse(formatter, output.Response{
			Command:   "app deploy",
			Status:    "blocked",
			Summary:   "Nginx enabled site 已存在且不属于当前 app",
			Fields:    []output.Field{{Label: "details", Value: err.Error()}},
			NextSteps: []string{"检查冲突的 Nginx enabled site，确认后手动迁移、删除，或更换 app.name。"},
		})
	}

	if cfg.RequiresTailscale() {
		tailscaleResult, err := ensureAppTailscale(stdcontext.Background(), cfg, privilegedExecutor)
		effects.AddTailscaleResult(tailscaleResult)
		if err != nil {
			return appDeployFailureWithEffects(formatter, "Tailscale client 前置条件失败", err, effects)
		}
	}

	effects.AddActions("started app host dependency check/install")
	if err := ensureAppHostDependencies(stdcontext.Background(), privilegedExecutor); err != nil {
		return appDeployFailureWithEffects(formatter, "安装 app 宿主机依赖失败", err, effects)
	}
	effects.AddActions("ensured app host dependencies")
	if _, err := privilegedExecutor.Systemctl(stdcontext.Background(), "enable", "--now", "nginx.service"); err != nil {
		return appDeployFailureWithEffects(formatter, "启动 Nginx service 失败", err, effects)
	}
	effects.AddActions("enabled and started Nginx service")
	if _, err := privilegedExecutor.Run(stdcontext.Background(), nginxcomponent.DisableDefaultSiteCommand()); err != nil {
		return appDeployFailureWithEffects(formatter, "禁用 Nginx 默认站点失败", err, effects)
	}
	effects.AddPaths(nginxcomponent.DefaultSiteEnabledPath)
	effects.AddActions("disabled distro Nginx default site if present")
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardDefaultServerCommand(names)); err != nil {
		return appDeployFailureWithEffects(formatter, "Nginx default_server 冲突", err, effects)
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardServerNameConflictsCommand(names, cfg.App.Domains)); err != nil {
		return appDeployFailureWithEffects(formatter, "Nginx server_name 冲突", err, effects)
	}
	if err := ensureAppNginxCompatibilityFn(stdcontext.Background(), cfg, privilegedExecutor); err != nil {
		return appDeployFailureWithEffects(formatter, "Nginx runtime 兼容性检查失败", err, effects)
	}
	effects.AddActions("checked Nginx runtime compatibility")
	if cfg.Mode() == appconfig.ModeListen {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardSystemUserCommand(names)); err != nil {
			return appDeployFailureWithEffects(formatter, "app system user/group 冲突", err, effects)
		}
		effects.AddActions("checked app system user/group")
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardRootDirectoriesCommand(names)); err != nil {
		return appDeployFailureWithEffects(formatter, "app 目录根所有权检查失败", err, effects)
	}
	effects.AddPaths(names.VarLibDir, names.VarLibMarkerPath, names.EtcDir, names.EtcMarkerPath, names.HookDir, names.HookDirMarkerPath)
	effects.AddActions("checked app root directory ownership")
	if cfg.Mode() == appconfig.ModeListen {
		for _, command := range appsvc.EnsureSystemUserCommands(names) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return appDeployFailureWithEffects(formatter, "创建或确认 app system user/group 失败", err, effects)
			}
		}
		effects.AddActions("ensured app system user/group")
		if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardServiceAccessCommand(names, cfg.ServiceBinary(), cfg.Service.WorkingDirectory)); err != nil {
			return appDeployFailureWithEffects(formatter, "app service 用户访问检查失败", err, effects)
		}
		effects.AddActions("checked app service user access")
	}
	for _, command := range appsvc.EnsureDirectoryCommands(names) {
		if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
			return appDeployFailureWithEffects(formatter, "创建 app 目录失败", err, effects)
		}
	}
	effects.AddPaths(names.WebrootPath, names.LegoDataPath, names.TLSDir)
	if _, err := privilegedExecutor.Run(stdcontext.Background(), appsvc.GuardTLSOwnershipCommand(names)); err != nil {
		return appDeployFailureWithEffects(formatter, "app TLS 证书目录所有权检查失败", err, effects)
	}
	effects.AddPaths(names.TLSMarkerPath)

	results, err := newAppFileInstallerFn(privilegedExecutor, privilege).Install(convertAppStagedFiles(staged))
	effects.AddPaths(host.CollectModifiedPaths(results)...)
	if err != nil {
		return appDeployFailureWithEffects(formatter, "写入 app runtime 文件失败", err, effects)
	}
	systemd := newHostSystemdFn(privilegedExecutor)
	if _, err := systemd.DaemonReload(stdcontext.Background()); err != nil {
		return appDeployFailureWithEffects(formatter, "systemd daemon-reload 失败", err, effects)
	}
	effects.AddActions("systemd daemon-reload")

	if cfg.App.ACMEChallenge == appconfig.ACMEChallengeHTTP01 {
		for _, command := range appsvc.HTTP01BootstrapCommands(names) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return appDeployFailureWithEffects(formatter, "准备 HTTP-01 临时证书失败", err, effects)
			}
		}
		effects.AddPaths(names.WebrootPath, names.LegoDataPath, names.TLSDir, names.FullchainPath, names.PrivateKeyPath)
		effects.AddActions("prepared HTTP-01 bootstrap certificate")
		if err := activateAppNginxTracking(stdcontext.Background(), privilegedExecutor, names, &effects); err != nil {
			return appDeployFailureWithEffects(formatter, "启用 app Nginx 站点失败", err, effects)
		}
	}
	certPlan, err := appsvc.NewCertificatePlan(cfg, names)
	if err != nil {
		return appDeployFailureWithEffects(formatter, "生成 app TLS 证书计划失败", err, effects)
	}
	if _, err := privilegedExecutor.Run(stdcontext.Background(), legocomponent.MigrationGateCommand(names.LegoDataPath)); err != nil {
		return appDeployFailureWithEffects(formatter, "迁移 app lego v5 storage 失败", err, effects)
	}
	if _, err := runHostCommandWithProgress(
		stdcontext.Background(),
		privilegedExecutor,
		certPlan.Command,
		ctx.stdout,
		format,
		dns01CertificateProgress("app deploy", cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01),
	); err != nil {
		return appDeployFailureWithEffects(formatter, "申请 app TLS 证书失败", err, effects)
	}
	effects.AddPaths(names.LegoDataPath, names.FullchainPath, names.PrivateKeyPath)
	effects.AddActions("issued or renewed app certificate")
	if cfg.App.ACMEChallenge != appconfig.ACMEChallengeHTTP01 {
		if err := activateAppNginxTracking(stdcontext.Background(), privilegedExecutor, names, &effects); err != nil {
			return appDeployFailureWithEffects(formatter, "启用 app Nginx 站点失败", err, effects)
		}
	}

	if cfg.Mode() == appconfig.ModeUpstream {
		removedPaths, err := removeStaleAppServiceUnit(stdcontext.Background(), privilegedExecutor, names)
		effects.AddPaths(removedPaths...)
		if len(removedPaths) > 0 {
			effects.AddActions("removed stale app systemd service")
		}
		if err != nil {
			return appDeployFailureWithEffects(formatter, "清理旧 app service 失败", err, effects)
		}
		if _, err := systemd.DaemonReload(stdcontext.Background()); err != nil {
			return appDeployFailureWithEffects(formatter, "systemd daemon-reload 失败", err, effects)
		}
		effects.AddActions("systemd daemon-reload")
	}

	if cfg.Mode() == appconfig.ModeListen {
		if _, err := systemd.Enable(stdcontext.Background(), names.ServiceUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "启用 app service 失败", err, effects)
		}
		effects.AddActions("enabled app systemd service")
		if _, err := systemd.Restart(stdcontext.Background(), names.ServiceUnit); err != nil {
			return appDeployFailureWithEffects(formatter, "重启 app service 失败", err, effects)
		}
		effects.AddActions("restarted app systemd service")
	}
	if _, err := systemd.Enable(stdcontext.Background(), names.RenewTimerUnit); err != nil {
		return appDeployFailureWithEffects(formatter, "启用 app 证书续期 timer 失败", err, effects)
	}
	effects.AddActions("enabled app certificate renewal timer")
	if _, err := systemd.Start(stdcontext.Background(), names.RenewTimerUnit); err != nil {
		return appDeployFailureWithEffects(formatter, "启动 app 证书续期 timer 失败", err, effects)
	}
	effects.AddActions("started app certificate renewal timer")

	fields := append(appConfigFields(options.configPath, cfg),
		output.Field{Label: "nginx site", Value: names.NginxAvailablePath},
		output.Field{Label: "renew timer", Value: names.RenewTimerUnit},
	)
	fields = append(fields, effects.Fields()...)
	fields = append(fields, appPreflightWarningFields(preflightReport)...)
	nextSteps := []string{
		fmt.Sprintf("运行 'meshify app verify --config %s' 复查静态配置和模板；再用 systemctl、nginx -t、证书和 tailscale status 验证宿主机状态。", options.configPath),
	}
	nextSteps = append(nextSteps, appPreflightWarningNextSteps(preflightReport)...)
	return formatter.Write(output.Response{
		Command:   "app deploy",
		Status:    "applied",
		Summary:   "app 已按配置部署或刷新",
		Fields:    fields,
		NextSteps: nextSteps,
	})
}

func appDeployRootRequiredResponse(permissions preflight.PermissionState) output.Response {
	detail := "fail: app deploy 需要当前进程具备 root 权限"
	if strings.TrimSpace(permissions.User) != "" {
		detail += "，当前用户: " + strings.TrimSpace(permissions.User)
	}
	return output.Response{
		Command:   "app deploy",
		Status:    "blocked",
		Summary:   "app deploy 预检发现 1 个失败项",
		Fields:    []output.Field{{Label: "check permissions", Value: detail}},
		NextSteps: []string{"使用 sudo meshify app deploy 重新执行。"},
	}
}

func loadAppConfigForResponse(path string, command string) (appconfig.Config, output.Response, bool) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return appconfig.Config{}, output.Response{
				Command: command,
				Status:  "missing-config",
				Summary: "未找到 app 配置文件",
				Fields:  []output.Field{{Label: "config path", Value: path}},
				NextSteps: []string{
					fmt.Sprintf("运行 'meshify app init --config %s' 生成示例配置。", path),
				},
			}, false
		}
		return appconfig.Config{}, output.Response{Command: command, Status: "failed", Summary: "读取 app 配置文件状态失败", Fields: []output.Field{{Label: "details", Value: err.Error()}}}, false
	}
	cfg, err := appconfig.LoadFile(path)
	if err != nil {
		return appconfig.Config{}, output.Response{
			Command: command,
			Status:  "invalid-config",
			Summary: "app 配置文件存在但校验失败",
			Fields:  []output.Field{{Label: "config path", Value: path}, {Label: "details", Value: err.Error()}},
			NextSteps: []string{
				fmt.Sprintf("修正 %s 后重新执行命令。", path),
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
	tcpBindings, tcpDetected := detectSSBindings("tcp", []int{80, 443})
	if !tcpDetected {
		return nil
	}
	bindings := make([]preflight.PortBinding, 0, 2)
	for _, port := range []int{80, 443} {
		if binding, ok := tcpBindings[port]; ok {
			bindings = append(bindings, binding)
			continue
		}
		bindings = append(bindings, preflight.PortBinding{Port: port, Protocol: "tcp"})
	}
	return bindings
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
	return true, true, "service.env_file 已通过 root-only 校验"
}

func detectAppTailscaleAuthKeyFileState(cfg appconfig.Config) (bool, bool, string) {
	path := strings.TrimSpace(cfg.Tailscale.AuthKeyFile)
	if path == "" {
		return false, false, ""
	}
	if _, err := tailscalecomponent.ReadAuthKeyFile(path); err != nil {
		return true, false, err.Error()
	}
	return true, true, "tailscale.auth_key_file 已通过 root-only 校验"
}

func inspectAppRootOnlyFile(field string, path string) (bool, string) {
	info, err := lstatAppServicePathFn(path)
	if err != nil {
		return false, field + " 不可用: " + err.Error()
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
	return true, field + " 已通过 root-only 校验"
}

func validateAppRootOnlyFileParents(field string, path string) error {
	dir := filepath.Dir(path)
	immediateParent := dir
	for {
		info, err := lstatAppServicePathFn(dir)
		if err != nil {
			return fmt.Errorf("%s parent directory %s 不可用: %w", field, dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s parent directory %s must not be a symlink", field, dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s parent path %s must be a directory", field, dir)
		}
		if info.Mode().Perm()&0o022 != 0 && (dir == immediateParent || info.Mode()&os.ModeSticky == 0) {
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
		content, err := fileSystem.ReadFile(file.HostPath)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
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

func ensureAppHostDependencies(ctx stdcontext.Context, executor host.Executor) error {
	if _, err := executor.AptGet(ctx, "update"); err != nil {
		return err
	}
	installArgs := append([]string{"install", "-y"}, appHostDependencyPackages()...)
	if _, err := executor.AptGet(ctx, installArgs...); err != nil {
		return err
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

func appHostDependencyPackages() []string {
	return []string{"nginx", "ca-certificates", "curl", "tar", "openssl"}
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
	if cfg.Mode() != appconfig.ModeListen {
		return nil
	}
	_, portString, err := net.SplitHostPort(cfg.App.Listen)
	if err != nil {
		return nil
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		return nil
	}
	metricsPort := mainCfg.Advanced.Headscale.MetricsPort
	if metricsPort > 0 && port == metricsPort {
		return fmt.Errorf("app.listen must not reuse Headscale metrics port %d from %s", metricsPort, mainConfigPath)
	}
	return nil
}

func appMainConfigConflictResponse(configPath string, command string, err error) output.Response {
	return output.Response{
		Command: command,
		Status:  "invalid-config",
		Summary: "app 配置与主 Headscale server_url 或 metrics_port 冲突",
		Fields: []output.Field{
			{Label: "config path", Value: configPath},
			{Label: "details", Value: err.Error()},
		},
		NextSteps: []string{"更换 app.domains 或 app.listen，确保 app 使用独立域名且不复用主 Headscale 端口。"},
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
			return "", fmt.Errorf("tailscale.auth_key_file 不可用: %w", err)
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

func activateAppNginx(ctx stdcontext.Context, executor host.Executor, names appsvc.Names) error {
	return activateAppNginxTracking(ctx, executor, names, nil)
}

func activateAppNginxTracking(ctx stdcontext.Context, executor host.Executor, names appsvc.Names, effects *appDeployEffects) error {
	for _, command := range []host.Command{appsvc.GuardEnabledSiteCommand(names), appsvc.EnableSiteCommand(names), appsvc.TestNginxCommand(), appsvc.ReloadNginxCommand()} {
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
		case "systemctl":
			effects.AddActions("reloaded Nginx")
		}
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
		NextSteps: []string{"修复错误后重新执行同一条 meshify app deploy 命令。"},
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
		"meshify app 管理附加 Go 服务和 tailnet upstream。",
		"",
		"用法:",
		"  meshify app <command> [flags]",
		"",
		"命令:",
		"  init    生成可编辑的 app 示例配置。",
		"  deploy  按配置部署一个 app。",
		"  verify  校验 app 配置和 runtime 模板。",
	)
}

func writeAppInitHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"生成可编辑的 app 示例配置。",
		"",
		"用法:",
		"  meshify app init [--config path] [--format human|json]",
		"",
		"参数:",
		"  --config string   要创建的 meshify app 配置文件路径。",
		"  --format string   输出格式：human | json",
	)
}

func writeAppVerifyHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"校验 app 配置和 runtime 模板。",
		"",
		"用法:",
		"  meshify app verify [--config path] [--format human|json]",
		"",
		"参数:",
		"  --config string   meshify app 配置文件路径。",
		"  --format string   输出格式：human | json",
	)
}

func writeAppDeployHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"按配置部署一个 app。",
		"",
		"用法:",
		"  meshify app deploy [--config path] [--format human|json]",
		"",
		"参数:",
		"  --config string   meshify app 配置文件路径。",
		"  --format string   输出格式：human | json",
	)
}
