package cli

import (
	"bufio"
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"meshify/internal/assets"
	"meshify/internal/components/headscale"
	"meshify/internal/components/nginx"
	"meshify/internal/config"
	"meshify/internal/host"
	"meshify/internal/output"
	"meshify/internal/preflight"
	"meshify/internal/render"
	"meshify/internal/state"
	"meshify/internal/verify"
	"meshify/internal/workflow"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	acmecatalog "meshify/internal/acme"

	legocomponent "meshify/internal/components/lego"

	tlscomponent "meshify/internal/components/tls"

	"golang.org/x/net/http/httpproxy"
)

type stagedFileInstaller interface {
	Install(files []render.StagedFile) ([]host.FileInstallResult, error)
}

type headscalePackageInstaller interface {
	Install(ctx stdcontext.Context, plan headscale.InstallPlan) ([]host.Result, error)
}

type legoInstaller interface {
	Install(ctx stdcontext.Context, plan legocomponent.InstallPlan) ([]host.Result, error)
}

type nginxSiteActivator interface {
	EnableTestAndReload(ctx stdcontext.Context) ([]host.Result, error)
}

type headscaleOnboarder interface {
	CreatePreAuthKey(ctx stdcontext.Context, plan headscale.OnboardingPlan) (string, []host.Result, error)
}

const (
	deployCheckpointPackageManagerReady          = "package-manager-ready"
	deployCheckpointHostDependenciesInstalled    = "host-dependencies-installed"
	deployCheckpointPackageArchitectureConfirmed = "package-architecture-confirmed"
	deployCheckpointLegoInstalled                = "lego-installed"
	deployCheckpointHeadscalePackageInstalled    = "headscale-package-installed"
	deployCheckpointRuntimeAssetsInstalled       = "runtime-assets-installed"
	deployCheckpointTLSBootstrapReady            = "tls-bootstrap-ready"
	deployCheckpointLegoCommandReady             = "lego-command-ready"
	deployCheckpointLegoCommandDeferred          = "lego-command-deferred"
	deployCheckpointCertificateIssued            = "certificate-issued"
	deployCheckpointNginxActivated               = "nginx-site-activated"
	deployCheckpointSystemdDaemonReloaded        = "systemd-daemon-reloaded"
	deployCheckpointSystemdDaemonReloadDeferred  = "systemd-daemon-reload-deferred"
	deployCheckpointServicesEnabled              = "services-enabled"
	deployCheckpointOnboardingReady              = "onboarding-ready"
	deployCheckpointStaticVerifyPassed           = "static-verify-passed"
)

var (
	collectDeployPreflightInputs  = defaultDeployPreflightInputs
	detectPermissionStateFn       = detectPermissionState
	detectPlatformInfoFn          = detectPlatformInfo
	detectHostCapabilityStateFn   = detectHostCapabilityState
	detectDNSProbeFn              = detectDNSProbe
	detectPortBindingsFn          = detectPortBindings
	detectFirewallStateFn         = detectFirewallState
	detectServiceStatesFn         = detectServiceStates
	detectPackageSourceStateFn    = detectPackageSourceState
	detectACMEStateFn             = detectACMEState
	probePackageURLFn             = probePackageURL
	hashRemoteArtifactFn          = hashRemoteArtifact
	lookupOfficialPackageDigestFn = lookupOfficialPackageDigest
	stageRuntimeFilesFn           = render.StageRuntime
	statDNSCredentialsFileFn      = os.Stat
	readDNSCredentialsFileFn      = os.ReadFile
	newDeployFileInstallerFn      = func(executor host.Executor, privilege host.PrivilegeStrategy) stagedFileInstaller {
		if privilege.RequiresSudo() {
			return host.NewFileInstaller(host.NewCommandFileSystem(executor), "")
		}
		return host.NewFileInstaller(nil, "")
	}
	checkpointPathForConfigFn  = defaultCheckpointPath
	checkpointStoreForConfigFn = func(configPath string) state.Store { return state.NewStore(checkpointPathForConfigFn(configPath)) }
	newHostExecutorFn          = func(env map[string]string) host.Executor { return host.NewExecutor(nil, env) }
	newHostSystemdFn           = func(executor host.Executor) host.Systemd { return host.NewSystemd(executor) }
	newHeadscaleInstallerFn    = func(executor host.Executor) headscalePackageInstaller { return headscale.NewInstaller(executor) }
	newLegoInstallerFn         = func(executor host.Executor) legoInstaller { return legocomponent.NewInstaller(executor) }
	newNginxActivatorFn        = func(executor host.Executor) nginxSiteActivator { return nginx.NewActivator(executor) }
	newHeadscaleOnboarderFn    = func(executor host.Executor) headscaleOnboarder { return headscale.NewOnboarding(executor) }
)

var requiredFirewallPorts = []string{"80/tcp", "443/tcp", "3478/udp"}

var knownConflictServices = []string{"headscale", "nginx", "apache2", "caddy", "traefik"}

var (
	ufwStatusColumnSeparator = regexp.MustCompile(`[[:space:]]{2,}`)
	dnsEnvFileKeyPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

var ufwApplicationProfilePorts = map[string][]string{
	"nginx full":  {"80/tcp", "443/tcp"},
	"nginx http":  {"80/tcp"},
	"nginx https": {"443/tcp"},
}

func newDeployCommand() command {
	return command{
		summary: "Run preflight checks and apply the Headscale, Nginx, TLS, service, and onboarding workflow.",
		usage:   writeDeployHelp,
		run:     runDeploy,
	}
}

func runDeploy(ctx context, args []string) error {
	flagSet := newFlagSet("deploy")
	options := sharedOptions{configPath: DefaultConfigPath, formatValue: string(output.FormatHuman)}
	options.bind(flagSet, "Path to the meshify config file.")

	shown, err := parseFlags(flagSet, args, writeDeployHelp, ctx.stdout)
	if err != nil {
		return fmt.Errorf("parse deploy flags: %w", err)
	}
	if shown {
		return nil
	}
	if err := rejectPositionalArgs("deploy", flagSet); err != nil {
		return err
	}

	format, err := output.ParseFormat(options.formatValue)
	if err != nil {
		return err
	}
	formatter := output.NewFormatter(ctx.stdout, format)
	checkpointPath := checkpointPathForConfigFn(options.configPath)
	checkpointStore := checkpointStoreForConfigFn(options.configPath)

	if _, err := os.Stat(options.configPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return formatter.Write(output.Response{
				Command: "deploy",
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
			Command: "deploy",
			Status:  "invalid-config",
			Summary: "config file exists but failed validation",
			Fields: []output.Field{
				{Label: "config path", Value: options.configPath},
				{Label: "details", Value: err.Error()},
			},
			NextSteps: []string{
				fmt.Sprintf("Fix the config at %s and rerun 'meshify deploy --config %s'.", options.configPath, options.configPath),
			},
		})
	}

	checkpoint, err := checkpointStore.Load()
	if err != nil {
		return formatCheckpointLoadFailure(formatter, "deploy", options.configPath, checkpointPath, err)
	}

	desiredStateDigest, err := deployDesiredStateDigest(cfg)
	if err != nil {
		return writeDeployFailure(formatter, checkpointStore, checkpoint, desiredStateDigestFailure("deploy", options.configPath, err))
	}
	checkpoint.BeginDeploy(desiredStateDigest)

	preflightInputs := collectDeployPreflightInputs(cfg)
	report := preflight.BuildReport(cfg, preflightInputs)
	diagnostics := output.NewDiagnosticsFormatter(ctx.stdout, format)
	if err := diagnostics.WriteReport("deploy", report); err != nil {
		return err
	}
	if report.FailedCount() > 0 || report.ManualCount() > 0 {
		checkpoint.RecordFailure(workflow.Failure{
			Step:         "preflight",
			Operation:    report.Summary(),
			Impact:       "deploy cannot continue until the blocking preflight items are resolved",
			Remediation:  report.NextSteps(),
			RetryCommand: deployRetryCommand(options.configPath),
		}.Snapshot())
		if err := checkpointStore.Save(checkpoint); err != nil {
			return fmt.Errorf("save checkpoint: %w", err)
		}
		return workflow.Failure{Step: "preflight", Operation: report.Summary()}
	}

	privilege := deployPrivilegeStrategy(preflightInputs.Permissions)
	executor := newHostExecutorFn(deployProxyEnv(cfg))
	privilegedExecutor := executor.WithPrivilege(privilege)
	systemd := newHostSystemdFn(privilegedExecutor)
	var results []host.FileInstallResult
	var stagedFiles []render.StagedFile
	var onboardingKey string
	headscaleRestarted := false
	if !deployPhaseCompleted(checkpoint, deployCheckpointPackageManagerReady) {
		if _, err := executor.AptGet(stdcontext.Background(), "--version"); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "check package manager",
				Operation:    "running apt-get --version to confirm host package manager access",
				Impact:       "package-backed host changes cannot continue until apt-get is available",
				Remediation:  []string{"Confirm apt-get is installed and reachable in PATH, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointPackageManagerReady, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointPackageArchitectureConfirmed) {
		packageArchResult, err := executor.Dpkg(stdcontext.Background(), "--print-architecture")
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "confirm package architecture",
				Operation:    "collecting host package architecture via dpkg",
				Impact:       "meshify cannot choose the right package inputs until dpkg reports host architecture",
				Remediation:  []string{"Confirm dpkg is installed and reachable in PATH, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		expectedArch := packageArch(cfg)
		if detectedArch := strings.TrimSpace(packageArchResult.Stdout); detectedArch != "" && detectedArch != expectedArch {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:      "confirm package architecture",
				Operation: fmt.Sprintf("matching dpkg architecture %q to config target %q", detectedArch, expectedArch),
				Impact:    "meshify cannot safely continue until package architecture matches the target host",
				Remediation: []string{
					fmt.Sprintf("Update advanced.platform.arch to %s or rerun deploy on a matching host.", detectedArch),
				},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        fmt.Errorf("dpkg reported %s while config expects %s", detectedArch, expectedArch),
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointPackageArchitectureConfirmed, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointHostDependenciesInstalled) {
		dependencyPackages, err := deployHostDependencyPackages(cfg)
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "plan host dependencies",
				Operation:    "selecting Nginx and archive installation helper packages",
				Impact:       "meshify cannot install HTTPS ingress and pinned release artifacts until host dependencies are known",
				Remediation:  []string{"Use a supported platform architecture or switch Headscale source settings, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if _, err := privilegedExecutor.AptGet(stdcontext.Background(), "update"); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "install host dependencies",
				Operation:    "refreshing package metadata before installing Nginx and artifact helper packages",
				Impact:       "meshify cannot install the reverse proxy and pinned release artifacts until package metadata refresh succeeds",
				Remediation:  []string{"Fix apt repository access, proxy settings, or package locks, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		installArgs := append([]string{"install", "-y"}, dependencyPackages...)
		if _, err := privilegedExecutor.AptGet(stdcontext.Background(), installArgs...); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "install host dependencies",
				Operation:    "installing Nginx and artifact helper packages through apt-get",
				Impact:       "meshify cannot configure HTTPS ingress or install pinned release artifacts until host dependencies are installed",
				Remediation:  []string{"Fix apt repository access or install the listed dependency packages manually, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointHostDependenciesInstalled, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointLegoInstalled) {
		installPlan, err := legocomponent.NewInstallPlan(cfg, legocomponent.InstallPlanOptions{})
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "plan lego install",
				Operation:    "selecting the pinned lego v4.35.2 Linux archive source and SHA-256 digest",
				Impact:       "meshify cannot continue certificate automation until the lego release artifact is fully pinned",
				Remediation:  []string{"Use advanced.platform.arch amd64 or arm64, fix advanced.lego_source settings, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if _, err := newLegoInstallerFn(privilegedExecutor).Install(stdcontext.Background(), installPlan); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "install lego binary",
				Operation:    "verifying and installing the pinned lego v4.35.2 archive to /opt/meshify/bin/lego",
				Impact:       "meshify cannot continue ACME automation until the pinned lego binary is installed",
				Remediation:  []string{"Fix GitHub release reachability, proxy settings, advanced.lego_source.file_path, archive permissions, or digest mismatches, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointLegoInstalled, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointHeadscalePackageInstalled) {
		installPlan, err := headscale.NewInstallPlan(cfg, headscale.InstallPlanOptions{
			VerifiedPackageSHA256: strings.TrimSpace(preflightInputs.PackageSource.ExpectedSHA256),
		})
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "plan Headscale package install",
				Operation:    "building the verified Headscale v0.28.0 package install plan",
				Impact:       "meshify cannot install Headscale until Headscale source metadata is complete",
				Remediation:  []string{"Fix advanced.headscale_source settings or rerun preflight with reachable package metadata."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if _, err := newHeadscaleInstallerFn(privilegedExecutor).Install(stdcontext.Background(), installPlan); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "install Headscale package",
				Operation:    "installing the verified Headscale v0.28.0 .deb package",
				Impact:       "meshify cannot continue to service configuration until Headscale installs successfully",
				Remediation:  []string{"Fix package download, checksum, or apt installation errors, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointHeadscalePackageInstalled, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointRuntimeAssetsInstalled) {
		var err error
		stagedFiles, err = stageDeployFiles(cfg)
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "render runtime assets",
				Operation:    "building the current runtime asset set",
				Impact:       "deploy cannot continue until the runtime templates render cleanly",
				Remediation:  []string{"Fix the config values or runtime templates and rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}

		results, err = newDeployFileInstallerFn(privilegedExecutor, privilege).Install(stagedFiles)
		checkpoint.RecordModifiedPaths(host.CollectModifiedPaths(results)...)
		checkpoint.RecordActivations(host.CollectActivations(results)...)
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "install runtime assets",
				Operation:    "writing runtime files to host paths",
				Impact:       "deploy may be partially applied until the blocking host error is resolved",
				Remediation:  []string{"Inspect the persisted checkpoint, correct the host filesystem issue, and rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}

		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointRuntimeAssetsInstalled, options.configPath); err != nil {
			return err
		}
	}

	if cfg.Default.ACMEChallenge == config.ACMEChallengeHTTP01 && !deployPhaseCompleted(checkpoint, deployCheckpointTLSBootstrapReady) {
		certificatePlan, err := tlscomponent.NewCertificatePlan(cfg)
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "plan HTTP-01 bootstrap",
				Operation:    "building the temporary certificate and webroot preparation commands",
				Impact:       "meshify cannot prepare Nginx for first HTTP-01 issuance until TLS inputs are valid",
				Remediation:  []string{"Fix default.server_url, default.certificate_email, or ACME settings, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		for _, command := range tlscomponent.HTTP01BootstrapCommands(certificatePlan.ServerName) {
			if _, err := privilegedExecutor.Run(stdcontext.Background(), command); err != nil {
				return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
					Step:         "prepare HTTP-01 bootstrap",
					Operation:    "creating the ACME webroot and temporary certificate for initial Nginx activation",
					Impact:       "meshify cannot serve HTTP-01 challenges through Nginx until bootstrap files are ready",
					Remediation:  []string{"Fix filesystem permissions or openssl availability, then rerun deploy."},
					RetryCommand: deployRetryCommand(options.configPath),
					Cause:        err,
				})
			}
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointTLSBootstrapReady, options.configPath); err != nil {
			return err
		}
	}

	if cfg.Default.ACMEChallenge == config.ACMEChallengeHTTP01 && deployPhaseCompleted(checkpoint, deployCheckpointTLSBootstrapReady) && !deployPhaseCompleted(checkpoint, deployCheckpointNginxActivated) {
		if _, err := newNginxActivatorFn(privilegedExecutor).EnableTestAndReload(stdcontext.Background()); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "activate Nginx site",
				Operation:    "enabling the Headscale Nginx site, testing config, and reloading Nginx",
				Impact:       "meshify cannot expose the HTTP-01 webroot, HTTPS control plane, or DERP WebSocket endpoint until Nginx accepts the site",
				Remediation:  []string{"Fix the Nginx config test output, conflicting default_server sites, certificate paths, or service reload issue, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointNginxActivated, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointLegoCommandReady) {
		legoResult, err := executor.Run(stdcontext.Background(), host.Command{Name: legocomponent.BinaryPath, Args: []string{"--version"}})
		if err != nil {
			if host.CommandMissing(legoResult, err, legocomponent.BinaryPath, "lego") {
				if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointLegoCommandDeferred, options.configPath); err != nil {
					return err
				}
				return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
					Step:         "check lego command",
					Operation:    "running /opt/meshify/bin/lego --version to confirm certificate tooling reachability",
					Impact:       "meshify cannot issue the public TLS certificate, activate the final HTTPS site, or complete deploy until lego is available",
					Remediation:  []string{"Rerun deploy so meshify can reinstall the pinned lego binary, or fix /opt/meshify/bin/lego permissions."},
					RetryCommand: deployRetryCommand(options.configPath),
					Cause:        err,
				})
			} else {
				return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
					Step:         "check lego command",
					Operation:    "running /opt/meshify/bin/lego --version to confirm certificate tooling reachability",
					Impact:       "certificate-related host changes cannot continue until lego commands succeed",
					Remediation:  []string{"Rerun deploy so meshify can reinstall the pinned lego binary, or fix /opt/meshify/bin/lego permissions."},
					RetryCommand: deployRetryCommand(options.configPath),
					Cause:        err,
				})
			}
		} else {
			removeDeployCheckpoint(&checkpoint, deployCheckpointLegoCommandDeferred)
			if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointLegoCommandReady, options.configPath); err != nil {
				return err
			}
		}
	}

	if deployPhaseCompleted(checkpoint, deployCheckpointLegoCommandReady) && !deployPhaseCompleted(checkpoint, deployCheckpointCertificateIssued) {
		certificatePlan, err := tlscomponent.NewCertificatePlan(cfg)
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "plan certificate issuance",
				Operation:    "building the lego command for the configured ACME challenge",
				Impact:       "meshify cannot request the public TLS certificate until ACME inputs are valid",
				Remediation:  []string{"Fix default.acme_challenge, default.certificate_email, or DNS-01 provider settings, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if _, err := privilegedExecutor.Run(stdcontext.Background(), certificatePlan.Command); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "issue certificate",
				Operation:    "running lego for the Headscale public hostname",
				Impact:       "meshify cannot activate the HTTPS Nginx site until a fullchain certificate is available",
				Remediation:  []string{"Fix ACME reachability, DNS-01 credentials, or rate-limit issues, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointCertificateIssued, options.configPath); err != nil {
			return err
		}
	}

	if cfg.Default.ACMEChallenge != config.ACMEChallengeHTTP01 && deployPhaseCompleted(checkpoint, deployCheckpointCertificateIssued) && !deployPhaseCompleted(checkpoint, deployCheckpointNginxActivated) {
		if _, err := newNginxActivatorFn(privilegedExecutor).EnableTestAndReload(stdcontext.Background()); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "activate Nginx site",
				Operation:    "enabling the Headscale Nginx site, testing config, and reloading Nginx",
				Impact:       "meshify cannot expose the HTTPS control plane or DERP WebSocket endpoint until Nginx accepts the site",
				Remediation:  []string{"Fix the Nginx config test output, conflicting default_server sites, certificate paths, or service reload issue, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointNginxActivated, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointSystemdDaemonReloaded) {
		systemdResult, err := systemd.DaemonReload(stdcontext.Background())
		if err != nil {
			if systemdCommandDeferred(systemdResult, err) {
				if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointSystemdDaemonReloadDeferred, options.configPath); err != nil {
					return err
				}
				return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
					Step:         "reload systemd",
					Operation:    "running systemctl daemon-reload to confirm service manager reachability",
					Impact:       "meshify cannot enable services, start the renewal timer, or prepare onboarding until systemd is available",
					Remediation:  []string{"Run deploy on a booted systemd host, or fix systemctl bus access, then rerun deploy."},
					RetryCommand: deployRetryCommand(options.configPath),
					Cause:        err,
				})
			} else {
				return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
					Step:         "reload systemd",
					Operation:    "running systemctl daemon-reload to confirm service manager reachability",
					Impact:       "service-backed host changes cannot continue until systemctl accepts deploy commands",
					Remediation:  []string{"Confirm the host is running systemd and that deploy has permission to talk to it, then rerun deploy."},
					RetryCommand: deployRetryCommand(options.configPath),
					Cause:        err,
				})
			}
		} else {
			removeDeployCheckpoint(&checkpoint, deployCheckpointSystemdDaemonReloadDeferred)
			if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointSystemdDaemonReloaded, options.configPath); err != nil {
				return err
			}
		}
	}

	if deployPhaseCompleted(checkpoint, deployCheckpointSystemdDaemonReloaded) &&
		deployPhaseCompleted(checkpoint, deployCheckpointCertificateIssued) &&
		deployPhaseCompleted(checkpoint, deployCheckpointNginxActivated) &&
		!deployPhaseCompleted(checkpoint, deployCheckpointServicesEnabled) {
		if _, err := systemd.Enable(stdcontext.Background(), headscale.ServiceName, "nginx.service", tlscomponent.RenewTimer); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "enable services",
				Operation:    "enabling Headscale, Nginx, and meshify lego renewal systemd units",
				Impact:       "services or certificate renewals may not restart after reboot until systemd enablement succeeds",
				Remediation:  []string{"Fix systemd access or unit availability, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if _, err := systemd.Start(stdcontext.Background(), tlscomponent.RenewTimer); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "start renewal timer",
				Operation:    "starting the meshify lego renewal timer",
				Impact:       "certificate renewal will not run automatically until the timer starts",
				Remediation:  []string{"Inspect 'systemctl status meshify-lego-renew.timer', fix the timer unit, and rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		if _, err := systemd.Restart(stdcontext.Background(), headscale.ServiceName); err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "restart Headscale",
				Operation:    "restarting the Headscale systemd unit after runtime asset installation",
				Impact:       "clients cannot register or reconnect until Headscale starts with the rendered config",
				Remediation:  []string{"Inspect 'systemctl status headscale.service', fix the reported config or runtime error, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		headscaleRestarted = true
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointServicesEnabled, options.configPath); err != nil {
			return err
		}
	}

	if deployPhaseCompleted(checkpoint, deployCheckpointServicesEnabled) && !deployPhaseCompleted(checkpoint, deployCheckpointOnboardingReady) {
		if !headscaleRestarted {
			if _, err := systemd.Restart(stdcontext.Background(), headscale.ServiceName); err != nil {
				removeDeployCheckpoint(&checkpoint, deployCheckpointServicesEnabled)
				return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
					Step:         "restart Headscale",
					Operation:    "restarting the Headscale systemd unit before resumed onboarding",
					Impact:       "clients cannot register or reconnect until Headscale starts with the rendered config",
					Remediation:  []string{"Inspect 'systemctl status headscale.service --no-pager --full' and 'journalctl -u headscale.service -n 100 --no-pager', fix the reported config or runtime error, and rerun deploy."},
					RetryCommand: deployRetryCommand(options.configPath),
					Cause:        err,
				})
			}
		}
		onboardingPlan, err := headscale.NewOnboardingPlan(headscale.OnboardingOptions{})
		if err != nil {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "plan onboarding",
				Operation:    "building the local unix-socket onboarding plan",
				Impact:       "meshify cannot create the first user or preauthkey until onboarding inputs are valid",
				Remediation:  []string{"Fix onboarding defaults or create the first user manually with headscale, then rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		key, _, err := newHeadscaleOnboarderFn(privilegedExecutor).CreatePreAuthKey(stdcontext.Background(), onboardingPlan)
		if err != nil {
			removeDeployCheckpoint(&checkpoint, deployCheckpointServicesEnabled)
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "create onboarding preauthkey",
				Operation:    "creating the first Headscale user and preauthkey through local CLI management",
				Impact:       "server deployment may be complete, but clients cannot join non-interactively until a preauthkey exists",
				Remediation:  []string{"Inspect 'systemctl status headscale.service --no-pager --full' and 'journalctl -u headscale.service -n 100 --no-pager', fix Headscale service health, and rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
		onboardingKey = key
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointOnboardingReady, options.configPath); err != nil {
			return err
		}
	}

	if !deployPhaseCompleted(checkpoint, deployCheckpointStaticVerifyPassed) {
		if missing := missingRequiredDeployCheckpoints(checkpoint,
			deployCheckpointCertificateIssued,
			deployCheckpointNginxActivated,
			deployCheckpointSystemdDaemonReloaded,
			deployCheckpointServicesEnabled,
			deployCheckpointOnboardingReady,
		); len(missing) > 0 {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "complete deploy prerequisites",
				Operation:    "checking required deploy checkpoints before static verification",
				Impact:       "meshify cannot call the deployment ready until certificate issuance, Nginx activation, renewal scheduling, services, and onboarding complete",
				Remediation:  []string{"Rerun deploy after fixing the earlier deferred or failed host step."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        fmt.Errorf("missing required checkpoints: %s", strings.Join(missing, ", ")),
			})
		}
		if stagedFiles == nil {
			var err error
			stagedFiles, err = stageDeployFiles(cfg)
			if err != nil {
				return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
					Step:         "render runtime assets for verification",
					Operation:    "building the runtime asset set for static verification",
					Impact:       "meshify cannot verify runtime readiness until templates render cleanly",
					Remediation:  []string{"Fix the config values or runtime templates and rerun deploy."},
					RetryCommand: deployRetryCommand(options.configPath),
					Cause:        err,
				})
			}
		}
		verifyReport := verify.StaticReport(cfg, stagedFiles)
		if verifyReport.FailedCount() > 0 {
			return writeDeployFailure(formatter, checkpointStore, checkpoint, workflow.Failure{
				Step:         "verify runtime assets",
				Operation:    verifyReport.Summary(),
				Impact:       "meshify cannot call the deployment ready until static runtime checks pass",
				Remediation:  []string{"Run 'meshify verify' to inspect failed checks, fix the config or templates, and rerun deploy."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        fmt.Errorf("%s", verify.SummarizeChecks(verifyReport.Checks)),
			})
		}
		if err := recordDeployCheckpoint(formatter, checkpointStore, &checkpoint, deployCheckpointStaticVerifyPassed, options.configPath); err != nil {
			return err
		}
	}

	if checkpoint.FinalizeSuccessfulDeploy() {
		if err := checkpointStore.Save(checkpoint); err != nil {
			return formatDeployFailure(formatter, workflow.Failure{
				Step:         "finalize deploy checkpoint",
				Operation:    "retiring resumable deploy state after successful host changes",
				Impact:       "future deploy runs may continue using stale resume history until checkpoint persistence succeeds",
				Remediation:  []string{"Ensure the checkpoint directory is writable and rerun deploy so meshify can retire the stale resume state."},
				RetryCommand: deployRetryCommand(options.configPath),
				Cause:        err,
			})
		}
	}

	modifiedPaths := append([]string(nil), checkpoint.ModifiedPaths...)
	activations := append([]assets.Activation(nil), checkpoint.ActivationHistory...)
	summary := "preflight passed, server components were installed, runtime assets were applied, and verification checks passed"
	if len(host.CollectModifiedPaths(results)) == 0 {
		summary = "preflight passed; runtime assets already match the desired state and verification checks passed"
	}
	warnings := deferredCheckpointWarnings(checkpoint.CompletedCheckpoints)

	fields := append(configFields(options.configPath, cfg),
		output.Field{Label: "checkpoint path", Value: checkpointPath},
		output.Field{Label: "modified paths", Value: summarizeModifiedPaths(modifiedPaths)},
		output.Field{Label: "activation history", Value: joinActivations(activations)},
	)
	if len(checkpoint.CompletedCheckpoints) > 0 {
		fields = append(fields, output.Field{Label: "completed checkpoints", Value: strings.Join(checkpoint.CompletedCheckpoints, ", ")})
	}
	if len(warnings) > 0 {
		fields = append(fields, output.Field{Label: "warnings", Value: strings.Join(warnings, "; ")})
	}
	if strings.TrimSpace(onboardingKey) != "" {
		fields = append(fields, output.Field{Label: "preauth key", Value: onboardingKey})
	}

	return formatter.Write(output.Response{
		Command: "deploy",
		Status:  "applied",
		Summary: summary,
		Fields:  fields,
		NextSteps: []string{
			fmt.Sprintf("Use 'meshify status --config %s' to inspect persisted deploy context.", options.configPath),
			fmt.Sprintf("Use 'meshify verify --config %s' to re-run runtime asset and onboarding readiness checks.", options.configPath),
			"Join at least two clients from different networks with the preauth key and observe direct or DERP fallback paths.",
		},
	})
}

func writeDeployHelp(stdout io.Writer) error {
	return writeHelpLines(stdout,
		"Run preflight checks and apply the Headscale, Nginx, TLS, service, and onboarding workflow.",
		"",
		"Usage:",
		"  meshify deploy [--config path] [--format human|json]",
		"",
		"Flags:",
		"  --config string   Path to the meshify config file.",
		"  --format string   Output format: human | json",
	)
}

func defaultDeployPreflightInputs(cfg config.Config) preflight.Inputs {
	return preflight.Inputs{
		Permissions:   detectPermissionStateFn(),
		Platform:      detectPlatformInfoFn(),
		Capabilities:  detectHostCapabilityStateFn(),
		DNS:           detectDNSProbeFn(cfg.Default.ServerURL),
		Ports:         detectPortBindingsFn(cfg),
		Firewall:      detectFirewallStateFn(),
		Services:      detectServiceStatesFn(),
		PackageSource: detectPackageSourceStateFn(cfg),
		ACME:          detectACMEStateFn(cfg),
	}
}

func detectPermissionState() preflight.PermissionState {
	state := preflight.PermissionState{}
	if currentUser, err := user.Current(); err == nil {
		state.User = currentUser.Username
	}
	if os.Geteuid() == 0 {
		state.IsRoot = true
		return state
	}

	if _, err := exec.LookPath("sudo"); err == nil {
		state.SudoInstalled = true
		if _, err := newHostExecutorFn(nil).Run(stdcontext.Background(), host.Command{Name: "sudo", Args: []string{"-n", "true"}}); err == nil {
			state.SudoWorks = true
		}
	}

	return state
}

func deployPrivilegeStrategy(state preflight.PermissionState) host.PrivilegeStrategy {
	if !state.IsRoot && state.SudoWorks {
		return host.PrivilegeSudo
	}
	return host.PrivilegeDirect
}

func detectPlatformInfo() preflight.PlatformInfo {
	return parsePlatformInfoFromOSRelease(os.ReadFile, "/etc/os-release", "/usr/lib/os-release")
}

func parsePlatformInfoFromOSRelease(readFile func(string) ([]byte, error), paths ...string) preflight.PlatformInfo {
	for _, path := range paths {
		content, err := readFile(path)
		if err == nil {
			return preflight.ParseOSRelease(string(content))
		}
	}
	return preflight.PlatformInfo{}
}

func detectHostCapabilityState() preflight.HostCapabilityState {
	state := preflight.HostCapabilityState{}
	if path, ok := commandAvailable("apt-get"); ok {
		state.AptGetAvailable = true
		state.AptGetDetail = path
	} else {
		state.AptGetDetail = "not found in PATH"
	}
	if path, ok := commandAvailable("dpkg"); ok {
		state.DpkgAvailable = true
		state.DpkgDetail = path
	} else {
		state.DpkgDetail = "not found in PATH"
	}
	if path, ok := commandAvailable("systemctl"); ok {
		state.SystemctlAvailable = true
		state.SystemctlDetail = path
	} else {
		state.SystemctlDetail = "not found in PATH"
	}
	if info, err := os.Stat("/run/systemd/system"); err == nil && info.IsDir() {
		state.SystemdRuntimeAvailable = true
		state.SystemdRuntimeDetail = "/run/systemd/system exists"
	} else if err != nil {
		state.SystemdRuntimeDetail = err.Error()
	} else {
		state.SystemdRuntimeDetail = "/run/systemd/system is not a directory"
	}
	return state
}

func commandAvailable(name string) (string, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return path, true
}

func detectDNSProbe(serverURL string) preflight.DNSProbe {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return preflight.DNSProbe{}
	}

	host := parsedURL.Hostname()
	if host == "" {
		return preflight.DNSProbe{}
	}

	resolved, err := net.LookupHost(host)
	probe := preflight.DNSProbe{Host: host, ResolvedIPs: resolved}
	if err != nil {
		probe.LookupError = err.Error()
	}
	return probe
}

func detectPortBindings(cfg config.Config) []preflight.PortBinding {
	metricsPort := cfg.Advanced.Headscale.MetricsPort
	if metricsPort == 0 {
		metricsPort = config.DefaultHeadscaleMetricsPort
	}
	tcpPorts := []int{80, 443, 8080, metricsPort, 50443}
	tcpBindings, tcpDetected := detectSSBindings("tcp", uniqueInts(tcpPorts))
	udpBindings, udpDetected := detectSSBindings("udp", []int{3478})
	if !tcpDetected && !udpDetected {
		return nil
	}

	required := []preflight.PortBinding{
		{Port: 80, Protocol: "tcp"},
		{Port: 443, Protocol: "tcp"},
		{Port: 8080, Protocol: "tcp"},
		{Port: metricsPort, Protocol: "tcp"},
		{Port: 50443, Protocol: "tcp"},
		{Port: 3478, Protocol: "udp"},
	}
	bindings := make([]preflight.PortBinding, 0, len(required))
	for _, requiredBinding := range required {
		switch requiredBinding.Protocol {
		case "tcp":
			if !tcpDetected {
				continue
			}
			if binding, ok := tcpBindings[requiredBinding.Port]; ok {
				bindings = append(bindings, binding)
				continue
			}
		case "udp":
			if !udpDetected {
				continue
			}
			if binding, ok := udpBindings[requiredBinding.Port]; ok {
				bindings = append(bindings, binding)
				continue
			}
		}
		bindings = append(bindings, requiredBinding)
	}

	return bindings
}

func uniqueInts(values []int) []int {
	unique := make([]int, 0, len(values))
	seen := map[int]struct{}{}
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func detectSSBindings(protocol string, ports []int) (map[int]preflight.PortBinding, bool) {
	if _, err := exec.LookPath("ss"); err != nil {
		return nil, false
	}

	args := []string{"-H"}
	switch protocol {
	case "tcp":
		args = append(args, "-ltnp")
	case "udp":
		args = append(args, "-lunp")
	default:
		return nil, false
	}

	raw, err := exec.Command("ss", args...).CombinedOutput()
	if err != nil && len(raw) == 0 {
		return nil, false
	}
	return parseSSBindings(string(raw), protocol, ports)
}

func parseSSBindings(raw string, protocol string, ports []int) (map[int]preflight.PortBinding, bool) {
	portSet := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		portSet[port] = struct{}{}
	}

	bindings := make(map[int]preflight.PortBinding, len(ports))
	if strings.TrimSpace(raw) == "" {
		return bindings, true
	}

	parsedAny := false
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		port, ok := parseSocketPort(fields[3])
		if !ok {
			continue
		}
		parsedAny = true
		if _, wanted := portSet[port]; !wanted {
			continue
		}

		binding := preflight.PortBinding{Port: port, Protocol: protocol, InUse: true, Process: parseSocketProcessName(line)}
		if existing, ok := bindings[port]; ok && existing.Process != "" {
			continue
		}
		bindings[port] = binding
	}

	return bindings, parsedAny
}

func detectFirewallState() preflight.FirewallState {
	if _, err := exec.LookPath("ufw"); err == nil {
		return detectUFWFirewallState()
	}
	if _, err := exec.LookPath("firewall-cmd"); err == nil {
		return detectFirewalldState()
	}
	if _, err := exec.LookPath("nft"); err == nil {
		return detectNFTablesState()
	}
	return preflight.FirewallState{DetectionError: "No supported firewall backend was detected on this host."}
}

func detectUFWFirewallState() preflight.FirewallState {
	raw, err := exec.Command("ufw", "status").CombinedOutput()
	text := strings.TrimSpace(string(raw))
	state := preflight.FirewallState{Backend: "ufw"}
	if err != nil && text == "" {
		state.DetectionError = err.Error()
		return state
	}

	lowerText := strings.ToLower(text)
	if strings.Contains(lowerText, "status: inactive") {
		state.Inspected = true
		return state
	}
	if !strings.Contains(lowerText, "status: active") {
		state.DetectionError = commandProbeDetail(err, text)
		return state
	}

	state.Inspected = true
	state.Active = true
	state.AllowedPorts = parseUFWAllowedPorts(text)
	state.MissingPorts = missingFirewallPorts(state.AllowedPorts)
	return state
}

func detectFirewalldState() preflight.FirewallState {
	state := preflight.FirewallState{Backend: "firewalld"}
	rawState, err := exec.Command("firewall-cmd", "--state").CombinedOutput()
	stateText := strings.TrimSpace(string(rawState))
	if err != nil {
		if strings.Contains(strings.ToLower(stateText), "not running") {
			state.Inspected = true
			return state
		}
		state.DetectionError = commandProbeDetail(err, stateText)
		return state
	}

	state.Inspected = true
	if strings.TrimSpace(stateText) != "running" {
		return state
	}
	state.Active = true

	rawPorts, portsErr := exec.Command("firewall-cmd", "--list-ports").CombinedOutput()
	rawServices, servicesErr := exec.Command("firewall-cmd", "--list-services").CombinedOutput()
	if portsErr != nil && servicesErr != nil {
		state.Inspected = false
		state.DetectionError = commandProbeDetail(portsErr, strings.TrimSpace(string(rawPorts)))
		return state
	}

	allowed := append(parseDelimitedPorts(string(rawPorts)), mapFirewalldServicesToPorts(string(rawServices))...)
	state.AllowedPorts = uniqueStrings(allowed)
	state.MissingPorts = missingFirewallPorts(state.AllowedPorts)
	return state
}

func detectNFTablesState() preflight.FirewallState {
	raw, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
	text := strings.ToLower(strings.TrimSpace(string(raw)))
	state := preflight.FirewallState{Backend: "nftables"}
	if err != nil {
		state.DetectionError = commandProbeDetail(err, strings.TrimSpace(string(raw)))
		return state
	}

	state.Inspected = true
	if text == "" || !strings.Contains(text, "table ") {
		return state
	}
	state.Active = true

	allowed := []string{}
	for _, requiredPort := range requiredFirewallPorts {
		port, protocol, ok := splitLabeledPort(requiredPort)
		if !ok {
			continue
		}
		if nftRulesetAllowsPort(text, protocol, port) {
			allowed = append(allowed, requiredPort)
		}
	}
	state.AllowedPorts = uniqueStrings(allowed)
	state.MissingPorts = missingFirewallPorts(state.AllowedPorts)
	if len(state.MissingPorts) > 0 {
		state.Inspected = false
		state.DetectionError = "nftables ruleset is present but meshify could not confirm explicit allow rules for all required service ports."
	}
	return state
}

func detectServiceStates() []preflight.ServiceState {
	if services, ok := detectSystemdServiceStates(); ok {
		return services
	}
	if services, ok := detectPgrepServiceStates(); ok {
		return services
	}
	return nil
}

func detectSystemdServiceStates() ([]preflight.ServiceState, bool) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, false
	}

	executor := newHostExecutorFn(nil)
	systemd := newHostSystemdFn(executor)
	services := []preflight.ServiceState{}
	inspected := false
	for _, serviceName := range knownConflictServices {
		active, err := systemd.IsActive(stdcontext.Background(), serviceName)
		if err != nil {
			text := strings.TrimSpace(err.Error())
			if strings.Contains(text, "System has not been booted with systemd") || strings.Contains(text, "Failed to connect to bus") {
				return nil, false
			}
			continue
		}
		inspected = true
		if !active {
			continue
		}

		detail := "active"
		result, err := executor.Systemctl(stdcontext.Background(), "show", "--property=SubState", serviceName)
		text := strings.TrimSpace(result.Stdout)
		if text == "" {
			text = strings.TrimSpace(result.Stderr)
		}
		if strings.Contains(text, "System has not been booted with systemd") || strings.Contains(text, "Failed to connect to bus") {
			return nil, false
		}
		if err == nil {
			properties := parseSystemdProperties(text)
			if subState := strings.TrimSpace(properties["SubState"]); subState != "" {
				detail = subState
			}
		}

		services = append(services, preflight.ServiceState{
			Name:   serviceName,
			Active: true,
			Detail: detail,
		})
	}

	if !inspected {
		return nil, false
	}
	return services, true
}

func detectPgrepServiceStates() ([]preflight.ServiceState, bool) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		return nil, false
	}

	services := []preflight.ServiceState{}
	inspected := false
	for _, serviceName := range knownConflictServices {
		err := exec.Command("pgrep", "-x", serviceName).Run()
		if err == nil {
			inspected = true
			services = append(services, preflight.ServiceState{Name: serviceName, Active: true, Detail: "process detected"})
			continue
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			inspected = true
		}
	}

	if !inspected {
		return nil, false
	}
	return services, true
}

func detectPackageSourceState(cfg config.Config) preflight.PackageSourceState {
	probeClient := newDeployHTTPClient(cfg.Advanced.Proxy, 5*time.Second)
	artifactClient := newDeployHTTPClient(cfg.Advanced.Proxy, 20*time.Second)
	state := preflight.PackageSourceState{
		Mode:           strings.TrimSpace(cfg.Advanced.HeadscaleSource.Mode),
		Version:        strings.TrimSpace(cfg.Advanced.HeadscaleSource.Version),
		URL:            strings.TrimSpace(cfg.Advanced.HeadscaleSource.URL),
		FilePath:       strings.TrimSpace(cfg.Advanced.HeadscaleSource.FilePath),
		ExpectedSHA256: strings.TrimSpace(cfg.Advanced.HeadscaleSource.SHA256),
	}
	if legoArchive, err := legocomponent.NewArchivePlan(cfg, legocomponent.InstallPlanOptions{}); err == nil {
		state.LegoMode = legoArchive.Mode
		state.LegoVersion = legoArchive.Version
		state.LegoURL = legoArchive.SourceURL
		state.LegoFilePath = legoArchive.SourcePath
		state.LegoExpectedSHA256 = legoArchive.ExpectedSHA256
		switch legoArchive.Mode {
		case config.PackageSourceModeOffline:
			if state.LegoFilePath != "" {
				info, err := os.Stat(state.LegoFilePath)
				if err == nil && !info.IsDir() {
					state.LegoFileExists = true
					actualSHA256, err := hashLocalFile(state.LegoFilePath)
					if err == nil {
						state.LegoIntegrityChecked = true
						state.LegoActualSHA256 = actualSHA256
					} else {
						state.LegoReachabilityDetail = fmt.Sprintf("SHA-256 probe failed: %s", err)
					}
				}
			}
		default:
			state.LegoReachabilityChecked, state.LegoReachable, state.LegoReachabilityDetail = probePackageURLFn(probeClient, state.LegoURL)
			if state.LegoReachable && state.LegoExpectedSHA256 != "" {
				actualSHA256, err := hashRemoteArtifactFn(artifactClient, state.LegoURL)
				if err == nil {
					state.LegoIntegrityChecked = true
					state.LegoActualSHA256 = actualSHA256
				} else {
					state.LegoReachabilityDetail = appendProbeDetail(state.LegoReachabilityDetail, fmt.Sprintf("SHA-256 probe failed: %s", err))
				}
			}
		}
	} else {
		state.LegoReachabilityDetail = err.Error()
	}

	switch state.Mode {
	case config.PackageSourceModeDirect:
		packageURL := headscale.OfficialPackageURL(state.Version, packageArch(cfg))
		state.ReachabilityChecked, state.Reachable, state.ReachabilityDetail = probePackageURLFn(probeClient, packageURL)
		if state.Reachable && state.ExpectedSHA256 == "" {
			digest, err := lookupOfficialPackageDigestFn(artifactClient, state.Version, packageArch(cfg))
			if err == nil {
				state.ExpectedSHA256 = digest
			} else {
				state.ReachabilityDetail = appendProbeDetail(state.ReachabilityDetail, fmt.Sprintf("Official checksum lookup failed: %s", err))
			}
		}
		if state.Reachable && state.ExpectedSHA256 != "" {
			actualSHA256, err := hashRemoteArtifactFn(artifactClient, packageURL)
			if err == nil {
				state.IntegrityChecked = true
				state.ActualSHA256 = actualSHA256
			} else {
				state.ReachabilityDetail = appendProbeDetail(state.ReachabilityDetail, fmt.Sprintf("SHA-256 probe failed: %s", err))
			}
		}
	case config.PackageSourceModeMirror:
		state.ReachabilityChecked, state.Reachable, state.ReachabilityDetail = probePackageURLFn(probeClient, state.URL)
		if state.Reachable && state.ExpectedSHA256 != "" {
			actualSHA256, err := hashRemoteArtifactFn(artifactClient, state.URL)
			if err == nil {
				state.IntegrityChecked = true
				state.ActualSHA256 = actualSHA256
			} else {
				state.ReachabilityDetail = appendProbeDetail(state.ReachabilityDetail, fmt.Sprintf("SHA-256 probe failed: %s", err))
			}
		}
	case config.PackageSourceModeOffline:
		if state.FilePath == "" {
			return state
		}
		info, err := os.Stat(state.FilePath)
		if err != nil || info.IsDir() {
			return state
		}
		state.FileExists = true
		actualSHA256, err := hashLocalFile(state.FilePath)
		if err == nil {
			state.IntegrityChecked = true
			state.ActualSHA256 = actualSHA256
		}
	}

	return state
}

func detectACMEState(cfg config.Config) preflight.ACMEState {
	state := preflight.ACMEState{
		Challenge:            strings.TrimSpace(cfg.Default.ACMEChallenge),
		ServerHost:           parseServerHost(cfg.Default.ServerURL),
		CertificateEmail:     strings.TrimSpace(cfg.Default.CertificateEmail),
		DNSProvider:          strings.TrimSpace(cfg.Advanced.DNS01.Provider),
		DNSCredentialsFile:   strings.TrimSpace(cfg.Advanced.DNS01.CredentialsFile),
		DNSCredentialEnvFile: strings.TrimSpace(cfg.Advanced.DNS01.EnvFile),
	}

	switch state.Challenge {
	case config.ACMEChallengeHTTP01:
		if state.ServerHost == "" {
			return state
		}
		state.HTTP01Detail = "meshify verifies HTTP-01 challenge routing during deploy after installing and activating Nginx."
	case config.ACMEChallengeDNS01:
		if state.DNSProvider == "" {
			return state
		}
		state.DNSCredentialsChecked, state.DNSCredentialsReady, state.DNSCredentialsDetail = detectDNSCredentialState(cfg.Advanced.DNS01)
	}

	return state
}

func parseSocketPort(endpoint string) (int, bool) {
	lastColon := strings.LastIndex(strings.TrimSpace(endpoint), ":")
	if lastColon == -1 {
		return 0, false
	}
	port, err := strconv.Atoi(endpoint[lastColon+1:])
	if err != nil {
		return 0, false
	}
	return port, true
}

func parseSocketProcessName(line string) string {
	marker := "users:((\""
	index := strings.Index(line, marker)
	if index == -1 {
		return ""
	}
	remainder := line[index+len(marker):]
	end := strings.Index(remainder, "\"")
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(remainder[:end])
}

func parseUFWAllowedPorts(output string) []string {
	allowed := []string{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		columns := splitUFWStatusColumns(scanner.Text())
		if len(columns) < 2 || strings.ToUpper(columns[1]) != "ALLOW" {
			continue
		}
		allowed = append(allowed, expandUFWRuleTarget(columns[0])...)
	}
	return uniqueStrings(allowed)
}

func splitUFWStatusColumns(line string) []string {
	parts := ufwStatusColumnSeparator.Split(strings.TrimSpace(line), -1)
	columns := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		columns = append(columns, part)
	}
	return columns
}

func expandUFWRuleTarget(target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}

	normalized := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(target, "(v6)")))
	if ports, ok := ufwApplicationProfilePorts[normalized]; ok {
		return append([]string(nil), ports...)
	}

	fields := strings.Fields(target)
	if len(fields) == 0 {
		return nil
	}
	return expandDelimitedPortToken(fields[0])
}

func parseDelimitedPorts(raw string) []string {
	ports := []string{}
	for _, token := range strings.Fields(raw) {
		ports = append(ports, expandDelimitedPortToken(token)...)
	}
	return uniqueStrings(ports)
}

func expandDelimitedPortToken(token string) []string {
	token = strings.TrimSpace(token)
	if token == "" || !strings.Contains(token, "/") {
		return nil
	}
	portRange, protocol, ok := strings.Cut(token, "/")
	if !ok {
		return nil
	}
	ports := []string{}
	for _, part := range strings.Split(portRange, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ports = append(ports, fmt.Sprintf("%s/%s", part, strings.ToLower(strings.TrimSpace(protocol))))
	}
	return ports
}

func mapFirewalldServicesToPorts(output string) []string {
	allowed := []string{}
	for _, service := range strings.Fields(strings.ToLower(output)) {
		switch strings.TrimSpace(service) {
		case "http":
			allowed = append(allowed, "80/tcp")
		case "https":
			allowed = append(allowed, "443/tcp")
		case "stun":
			allowed = append(allowed, "3478/udp")
		}
	}
	return uniqueStrings(allowed)
}

func nftRulesetAllowsPort(ruleset string, protocol string, port int) bool {
	scanner := bufio.NewScanner(strings.NewReader(strings.ToLower(ruleset)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, protocol) || !strings.Contains(line, "accept") || !strings.Contains(line, "dport") {
			continue
		}
		if nftRuleLineAllowsPort(line, protocol, port) {
			return true
		}
	}
	return false
}

func nftRuleLineAllowsPort(line string, protocol string, port int) bool {
	tokens := nftRuleTokens(line)
	for index := 0; index+2 < len(tokens); index++ {
		if tokens[index] != protocol || tokens[index+1] != "dport" {
			continue
		}
		return nftPortExpressionAllows(tokens[index+2:], port)
	}
	return false
}

func nftRuleTokens(line string) []string {
	replacer := strings.NewReplacer("{", " { ", "}", " } ", ",", " , ")
	return strings.Fields(replacer.Replace(line))
}

func nftPortExpressionAllows(tokens []string, port int) bool {
	if len(tokens) == 0 {
		return false
	}
	if tokens[0] == "{" {
		for _, token := range tokens[1:] {
			if token == "}" {
				return false
			}
			if token == "," {
				continue
			}
			if nftPortTokenAllows(token, port) {
				return true
			}
		}
		return false
	}
	return nftPortTokenAllows(tokens[0], port)
}

func nftPortTokenAllows(token string, port int) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if startRaw, endRaw, ok := strings.Cut(token, "-"); ok {
		start, startErr := strconv.Atoi(strings.TrimSpace(startRaw))
		end, endErr := strconv.Atoi(strings.TrimSpace(endRaw))
		if startErr != nil || endErr != nil || start > end {
			return false
		}
		return port >= start && port <= end
	}
	value, err := strconv.Atoi(token)
	return err == nil && value == port
}

func missingFirewallPorts(allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}

	missing := make([]string, 0, len(requiredFirewallPorts))
	for _, requiredPort := range requiredFirewallPorts {
		if _, ok := allowedSet[strings.ToLower(requiredPort)]; ok {
			continue
		}
		missing = append(missing, requiredPort)
	}
	return missing
}

func parseSystemdProperties(output string) map[string]string {
	properties := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		properties[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return properties
}

func probePackageURL(client *http.Client, rawURL string) (bool, bool, string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false, false, ""
	}

	statusCode, finalURL, err := probeURL(client, rawURL, http.MethodHead)
	if err == nil && (statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented) {
		statusCode, finalURL, err = probeURL(client, rawURL, http.MethodGet)
	}
	if err != nil {
		return true, false, err.Error()
	}
	if statusCode >= http.StatusOK && statusCode < http.StatusBadRequest {
		return true, true, fmt.Sprintf("%s returned %d.", finalURL, statusCode)
	}
	return true, false, fmt.Sprintf("%s returned %d.", finalURL, statusCode)
}

func packageArch(cfg config.Config) string {
	arch := strings.TrimSpace(cfg.Advanced.Platform.Arch)
	if arch == "" {
		arch = config.ArchAMD64
	}
	return arch
}

func deployHostDependencyPackages(cfg config.Config) ([]string, error) {
	return []string{"nginx", "ca-certificates", "curl", "tar", "openssl"}, cfg.Validate()
}

func hashRemoteArtifact(client *http.Client, rawURL string) (string, error) {
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "meshify-preflight/1.0")

	client = httpClientOrDefault(client, 20*time.Second)
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("%s returned %s", response.Request.URL.String(), response.Status)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, response.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func lookupOfficialPackageDigest(client *http.Client, version string, arch string) (string, error) {
	assetName := headscale.OfficialPackageAssetName(version, arch)
	if assetName == "" {
		return "", fmt.Errorf("missing Headscale version or architecture")
	}

	checksums, err := fetchOfficialReleaseChecksums(client, version)
	if err != nil {
		return "", err
	}

	digest := strings.TrimSpace(checksums[assetName])
	if digest == "" {
		return "", fmt.Errorf("official checksum missing for %s", assetName)
	}
	return digest, nil
}

func fetchOfficialReleaseChecksums(client *http.Client, version string) (map[string]string, error) {
	checksumURL := headscale.OfficialChecksumsURL(version)
	if checksumURL == "" {
		return nil, fmt.Errorf("missing Headscale version")
	}

	request, err := http.NewRequest(http.MethodGet, checksumURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "meshify-preflight/1.0")

	client = httpClientOrDefault(client, 20*time.Second)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s returned %s", response.Request.URL.String(), response.Status)
	}

	checksums := map[string]string{}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		digest := strings.ToLower(strings.TrimSpace(fields[0]))
		assetName := strings.TrimSpace(fields[len(fields)-1])
		if len(digest) != 64 || assetName == "" {
			continue
		}
		checksums[assetName] = digest
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(checksums) == 0 {
		return nil, fmt.Errorf("%s did not contain any checksums", response.Request.URL.String())
	}

	return checksums, nil
}

func hashLocalFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
	}()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func stageDeployFiles(cfg config.Config) ([]render.StagedFile, error) {
	return stageRuntimeFilesFn(cfg)
}

func stageDeployFingerprintFiles(cfg config.Config) ([]render.StagedFile, error) {
	return stageRuntimeFilesFn(cfg)
}

func detectDNSCredentialState(dns01 config.DNS01Config) (bool, bool, string) {
	providerInfo, err := tlscomponent.DNSProvider(dns01.Provider)
	if err != nil {
		return false, false, err.Error()
	}

	envFile := strings.TrimSpace(dns01.EnvFile)
	if envFile != "" {
		env, ready, detail := inspectDNSEnvFile(envFile)
		if !ready {
			return true, false, fmt.Sprintf("DNS provider %q env_file is not ready: %s", providerInfo.LegoCode, detail)
		}
		if env != nil {
			if err := acmecatalog.ValidateDNSProviderEnvironment(providerInfo.LegoCode, env); err != nil {
				return true, false, err.Error()
			}
			if ready, detail := inspectDNSCredentialEnvFileReferences(providerInfo.LegoCode, env); !ready {
				return true, false, fmt.Sprintf("DNS provider %q env_file contains credential file references that are not ready: %s.", providerInfo.LegoCode, detail)
			}
		}
		return true, true, fmt.Sprintf("Using lego env_file for DNS provider %q: %s. %s", providerInfo.LegoCode, envFile, detail)
	}
	env := nonEmptyEnvironmentByKey()
	if providerInfo.LegoCode == "route53" && route53RawSecretEnvironmentPresent(env) && strings.TrimSpace(env["AWS_SHARED_CREDENTIALS_FILE"]) == "" {
		return true, false, "Detected Route53 AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY in the current environment, but meshify will not pass raw AWS secrets through sudo or systemd. Use advanced.dns01.env_file for DNS-01 deploy and renewal."
	}
	if providerInfo.AmbientCredentialsSupported {
		detail := fmt.Sprintf("Using lego ambient credential chain for DNS provider %q; confirm deploy and meshify-lego-renew.service run with the same host identity.", providerInfo.LegoCode)
		if providerInfo.LegoCode == "gcloud" {
			detail += " For gcloud, confirm Google Cloud metadata also provides the project, or set advanced.dns01.env_file with GCE_PROJECT."
		}
		return true, true, detail
	}
	return true, false, fmt.Sprintf("DNS provider %q requires advanced.dns01.env_file so initial issuance and meshify-lego-renew.service use the same provider environment.", providerInfo.LegoCode)
}

func inspectDNSCredentialsFile(filePath string) (bool, string) {
	info, err := statDNSCredentialsFileFn(filePath)
	if err != nil {
		if os.IsPermission(err) {
			return false, fmt.Sprintf("%s cannot be inspected by the current user; verify it is a root-owned private credentials file before retrying.", filePath)
		}
		return false, fmt.Sprintf("%s cannot be inspected: %v.", filePath, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Sprintf("%s is not a regular credentials file.", filePath)
	}
	if info.Size() == 0 {
		return false, fmt.Sprintf("%s is empty.", filePath)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Sprintf("%s must be readable only by root; remove group/other permissions before retrying.", filePath)
	}
	if uid, ok := fileOwnerUID(info); ok && uid != 0 {
		return false, fmt.Sprintf("%s must be owned by root before meshify uses it as a DNS credentials file.", filePath)
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsPermission(err) {
			return true, "Root-owned private credentials file exists but is not readable by the current user; lego will read it through deploy privileges."
		}
		return false, fmt.Sprintf("%s cannot be opened: %v.", filePath, err)
	}
	_ = file.Close()
	return true, "Root-owned private credentials file exists and is readable by the current user."
}

func inspectDNSEnvFile(filePath string) (map[string]string, bool, string) {
	ready, detail := inspectDNSCredentialsFile(filePath)
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
	return env, true, detail
}

func readDNSCredentialEnvFile(filePath string) ([]byte, string, error) {
	content, err := readDNSCredentialsFileFn(filePath)
	if err == nil {
		return content, "", nil
	}
	if !os.IsPermission(err) {
		return nil, "", err
	}

	content, detail, ok := readDNSCredentialEnvFileWithPrivilege(filePath)
	if !ok {
		return nil, "", fmt.Errorf("%s", detail)
	}
	return content, detail, nil
}

func readDNSCredentialEnvFileWithPrivilege(filePath string) ([]byte, string, bool) {
	permissions := detectPermissionStateFn()
	if !permissions.IsRoot && !permissions.SudoWorks {
		return nil, "the current user cannot read it and deploy privileges are not available for secret-safe validation", false
	}

	executor := newHostExecutorFn(nil).WithPrivilege(deployPrivilegeStrategy(permissions))
	result, err := executor.Run(stdcontext.Background(), host.Command{
		Name:        "cat",
		Args:        []string{"--", filePath},
		DisplayName: "cat",
		DisplayArgs: []string{"--", "<dns-env-file>"},
	})
	if err != nil {
		return nil, "deploy privileges could not read the env file without exposing secret values", false
	}
	return []byte(result.Stdout), "Validated env_file content through deploy privileges without printing secret values.", true
}

func parseDNSEnvFileContent(content []byte) (map[string]string, string) {
	env := map[string]string{}
	for lineNumber, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			return env, fmt.Sprintf("line %d uses shell export syntax", lineNumber+1)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !dnsEnvFileKeyPattern.MatchString(key) {
			return env, fmt.Sprintf("line %d uses an invalid environment variable name", lineNumber+1)
		}
		value = unquoteDNSEnvValue(value)
		if key == "" || value == "" {
			continue
		}
		env[key] = value
	}
	return env, ""
}

func unquoteDNSEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if first == last && (first == '"' || first == '\'') {
			return value[1 : len(value)-1]
		}
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

func route53RawSecretEnvironmentPresent(env map[string]string) bool {
	_, hasAccessKey := env["AWS_ACCESS_KEY_ID"]
	_, hasSecretKey := env["AWS_SECRET_ACCESS_KEY"]
	return hasAccessKey && hasSecretKey
}

func inspectDNSCredentialEnvFileReferences(provider string, env map[string]string) (bool, string) {
	invalidDetails := []string{}
	for key, value := range env {
		if !dnsCredentialEnvironmentValueIsFile(provider, key) {
			continue
		}
		if !filepath.IsAbs(value) {
			invalidDetails = append(invalidDetails, fmt.Sprintf("%s: referenced credential file path %q must be absolute so deploy and systemd renewal use the same runtime path", key, value))
			continue
		}
		ready, detail := inspectDNSCredentialsFile(value)
		if !ready {
			invalidDetails = append(invalidDetails, fmt.Sprintf("%s: %s", key, detail))
		}
	}
	invalidDetails = uniqueStrings(invalidDetails)
	sort.Strings(invalidDetails)
	if len(invalidDetails) > 0 {
		return false, strings.Join(invalidDetails, "; ")
	}
	return true, ""
}

func supportedDNSCredentialFileEnvKeys(provider string) map[string]struct{} {
	keys, err := acmecatalog.SupportedDNSProviderEnvFileVars(provider)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	return seen
}

func dnsCredentialEnvironmentValueIsFile(provider string, key string) bool {
	canonical, err := tlscomponent.CanonicalDNSProvider(provider)
	if err != nil {
		return false
	}

	key = strings.TrimSpace(key)
	switch key {
	case "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE", "GCE_SERVICE_ACCOUNT_FILE", "GOOGLE_APPLICATION_CREDENTIALS":
		return true
	}
	if canonical == "route53" && strings.HasPrefix(key, "AWS_") && strings.HasSuffix(key, "_FILE") {
		return false
	}
	if _, ok := supportedDNSCredentialFileEnvKeys(canonical)[key]; ok {
		return true
	}
	return false
}

func nonEmptyEnvironmentByKey() map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		env[key] = value
	}
	return env
}

func fileOwnerUID(info os.FileInfo) (uint64, bool) {
	sys := reflect.ValueOf(info.Sys())
	if !sys.IsValid() {
		return 0, false
	}
	if sys.Kind() == reflect.Pointer {
		if sys.IsNil() {
			return 0, false
		}
		sys = sys.Elem()
	}
	if sys.Kind() != reflect.Struct {
		return 0, false
	}
	uid := sys.FieldByName("Uid")
	if !uid.IsValid() {
		uid = sys.FieldByName("UID")
	}
	if !uid.IsValid() {
		return 0, false
	}
	switch uid.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return uid.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value := uid.Int()
		if value < 0 {
			return 0, false
		}
		return uint64(value), true
	default:
		return 0, false
	}
}

func defaultCheckpointPath(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	base := filepath.Base(configPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if name == "" {
		name = "meshify"
	}
	return filepath.Join(filepath.Dir(configPath), ".meshify", name+".checkpoint.json")
}

func deployRetryCommand(configPath string) string {
	return fmt.Sprintf("meshify deploy --config %s", configPath)
}

func deployProxyEnv(cfg config.Config) map[string]string {
	return host.ProxyEnv(
		cfg.Advanced.Proxy.HTTPProxy,
		cfg.Advanced.Proxy.HTTPSProxy,
		cfg.Advanced.Proxy.NoProxy,
	)
}

func newDeployHTTPClient(proxy config.ProxyConfig, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !deployProxyConfigured(proxy) {
		return &http.Client{Timeout: timeout, Transport: transport}
	}

	transport.Proxy = deployProxyFunc(proxy)
	return &http.Client{Timeout: timeout, Transport: transport}
}

func deployProxyConfigured(proxy config.ProxyConfig) bool {
	return strings.TrimSpace(proxy.HTTPProxy) != "" ||
		strings.TrimSpace(proxy.HTTPSProxy) != "" ||
		strings.TrimSpace(proxy.NoProxy) != ""
}

func httpClientOrDefault(client *http.Client, timeout time.Duration) *http.Client {
	if client != nil {
		return client
	}
	return newDeployHTTPClient(config.ProxyConfig{}, timeout)
}

func deployProxyFunc(proxy config.ProxyConfig) func(*http.Request) (*url.URL, error) {
	proxyForURL := (&httpproxy.Config{
		HTTPProxy:  strings.TrimSpace(proxy.HTTPProxy),
		HTTPSProxy: strings.TrimSpace(proxy.HTTPSProxy),
		NoProxy:    strings.TrimSpace(proxy.NoProxy),
	}).ProxyFunc()

	return func(request *http.Request) (*url.URL, error) {
		if request == nil || request.URL == nil {
			return nil, nil
		}
		return proxyForURL(request.URL)
	}
}

func deployPhaseCompleted(checkpoint state.Checkpoint, names ...string) bool {
	for _, name := range names {
		if checkpoint.HasCompleted(name) {
			return true
		}
	}
	return false
}

func removeDeployCheckpoint(checkpoint *state.Checkpoint, name string) {
	if checkpoint == nil {
		return
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return
	}
	filtered := checkpoint.CompletedCheckpoints[:0]
	for _, completed := range checkpoint.CompletedCheckpoints {
		if strings.TrimSpace(completed) == trimmed {
			continue
		}
		filtered = append(filtered, completed)
	}
	checkpoint.CompletedCheckpoints = filtered
	if strings.TrimSpace(checkpoint.CurrentCheckpoint) == trimmed {
		checkpoint.CurrentCheckpoint = ""
	}
}

func missingRequiredDeployCheckpoints(checkpoint state.Checkpoint, names ...string) []string {
	missing := []string{}
	for _, name := range names {
		if !deployPhaseCompleted(checkpoint, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

func recordDeployCheckpoint(formatter output.Formatter, store state.Store, checkpoint *state.Checkpoint, name string, configPath string) error {
	checkpoint.MarkCompleted(name)
	checkpoint.RecordFailure(workflow.FailureSnapshot{})
	if err := store.Save(*checkpoint); err != nil {
		return formatDeployFailure(formatter, workflow.Failure{
			Step:         "persist deploy checkpoint",
			Operation:    fmt.Sprintf("recording deploy checkpoint %s", name),
			Impact:       "meshify completed a host phase but could not persist the recovery point",
			Remediation:  []string{"Ensure the checkpoint directory is writable and rerun deploy."},
			RetryCommand: deployRetryCommand(configPath),
			Cause:        err,
		})
	}
	return nil
}

func writeDeployFailure(formatter output.Formatter, store state.Store, checkpoint state.Checkpoint, failure workflow.Failure) error {
	checkpoint.RecordFailure(failure.Snapshot())
	if err := store.Save(checkpoint); err != nil {
		return formatDeployFailureWithCheckpointWarning(formatter, failure, err)
	}
	return formatDeployFailure(formatter, failure)
}

func formatCheckpointLoadFailure(formatter output.Formatter, command string, configPath string, checkpointPath string, err error) error {
	return formatCheckpointLoadFailureWithFields(formatter, command, configPath, checkpointPath, err, nil)
}

func formatCheckpointLoadFailureWithFields(formatter output.Formatter, command string, configPath string, checkpointPath string, err error, prefixFields []output.Field) error {
	failure := workflow.Failure{
		Step:         "load deploy checkpoint",
		Operation:    "reading persisted deploy recovery state",
		Impact:       "meshify cannot use the saved recovery state until the checkpoint file can be read",
		RetryCommand: fmt.Sprintf("meshify %s --config %s", command, configPath),
		Cause:        err,
		Remediation: []string{
			fmt.Sprintf("Repair or remove the checkpoint at %s so meshify can continue.", checkpointPath),
			fmt.Sprintf("Fix the checkpoint path permissions and rerun 'meshify %s --config %s'.", command, configPath),
		},
	}

	var loadErr *state.LoadError
	if errors.As(err, &loadErr) && loadErr.Kind == state.LoadErrorDecode {
		failure.Impact = "meshify cannot trust the saved recovery state until the checkpoint file is repaired or removed"
		failure.Remediation = []string{
			fmt.Sprintf("Repair or remove the checkpoint at %s if you do not need to resume the previous deploy.", checkpointPath),
			fmt.Sprintf("Remove the unreadable checkpoint and rerun 'meshify %s --config %s' to regenerate recovery state.", command, configPath),
		}
	}

	response := failure.Response(command)
	if len(prefixFields) > 0 {
		response.Fields = append(append([]output.Field(nil), prefixFields...), response.Fields...)
	}
	response.Fields = append(response.Fields, output.Field{Label: "checkpoint path", Value: checkpointPath})
	if err := formatter.Write(response); err != nil {
		return err
	}
	return failure
}

func desiredStateDigestFailure(command string, configPath string, err error) workflow.Failure {
	impact := "meshify cannot compare or apply the current runtime asset set until the desired state fingerprint succeeds"
	if command == "status" {
		impact = "meshify cannot summarize deploy recovery state until the desired state fingerprint succeeds"
	}

	return workflow.Failure{
		Step:         "fingerprint desired state",
		Operation:    "building the current runtime asset fingerprint",
		Impact:       impact,
		RetryCommand: fmt.Sprintf("meshify %s --config %s", command, configPath),
		Cause:        err,
		Remediation: []string{
			"Fix the runtime template or config inputs that prevented staging the current runtime assets.",
		},
	}
}

func formatWorkflowFailure(formatter output.Formatter, command string, failure workflow.Failure) error {
	if err := formatter.Write(failure.Response(command)); err != nil {
		return err
	}
	return failure
}

func formatDeployFailure(formatter output.Formatter, failure workflow.Failure) error {
	return formatWorkflowFailure(formatter, "deploy", failure)
}

func formatDeployFailureWithCheckpointWarning(formatter output.Formatter, failure workflow.Failure, saveErr error) error {
	response := failure.Response("deploy")
	response.Fields = append(response.Fields, output.Field{
		Label: "checkpoint warning",
		Value: fmt.Sprintf("could not save recovery point: %s", summarizeDeployError(saveErr)),
	})
	response.NextSteps = append(response.NextSteps, "Recovery point was not saved; ensure the checkpoint directory is writable before rerunning deploy.")
	if err := formatter.Write(response); err != nil {
		return err
	}
	return fmt.Errorf("%s (could not save recovery point: %s)", failure.Summary(), summarizeDeployError(saveErr))
}

func summarizeDeployError(err error) string {
	if err == nil {
		return "unknown error"
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "unknown error"
	}
	line, _, _ := strings.Cut(message, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "unknown error"
	}
	return line
}

func joinActivations(activations []assets.Activation) string {
	if len(activations) == 0 {
		return "none"
	}

	labels := make([]string, 0, len(activations))
	for _, activation := range activations {
		labels = append(labels, string(activation))
	}
	return strings.Join(labels, ", ")
}

func summarizeModifiedPaths(paths []string) string {
	if len(paths) == 0 {
		return "none"
	}

	const maxPaths = 5
	shown := paths
	suffix := ""
	if len(paths) > maxPaths {
		shown = paths[:maxPaths]
		suffix = fmt.Sprintf(", ... (%d more; see checkpoint file for the full list)", len(paths)-maxPaths)
	}
	return fmt.Sprintf("%d total: %s%s", len(paths), strings.Join(shown, ", "), suffix)
}

func deployDesiredStateDigest(cfg config.Config) (string, error) {
	stagedFiles, err := stageDeployFingerprintFiles(cfg)
	if err != nil {
		return "", err
	}
	return deployDesiredStateDigestForStaged(cfg, stagedFiles)
}

func deployDesiredStateDigestForStaged(cfg config.Config, stagedFiles []render.StagedFile) (string, error) {
	type runtimeAssetFingerprint struct {
		SourcePath    string   `json:"source_path"`
		HostPath      string   `json:"host_path"`
		ContentMode   string   `json:"content_mode"`
		Mode          uint32   `json:"mode"`
		Activations   []string `json:"activations,omitempty"`
		ContentSHA256 string   `json:"content_sha256"`
	}
	type desiredStateFingerprint struct {
		Config       config.Config             `json:"config"`
		RuntimeFiles []runtimeAssetFingerprint `json:"runtime_files"`
	}

	runtimeFiles := make([]runtimeAssetFingerprint, 0, len(stagedFiles))
	for _, file := range stagedFiles {
		activations := make([]string, 0, len(file.Activations))
		for _, activation := range file.Activations {
			activations = append(activations, string(activation))
		}

		contentSum := sha256.Sum256(file.Content)
		runtimeFiles = append(runtimeFiles, runtimeAssetFingerprint{
			SourcePath:    file.SourcePath,
			HostPath:      file.HostPath,
			ContentMode:   string(file.ContentMode),
			Mode:          uint32(file.Mode.Perm()),
			Activations:   activations,
			ContentSHA256: hex.EncodeToString(contentSum[:]),
		})
	}
	sort.Slice(runtimeFiles, func(i, j int) bool {
		if runtimeFiles[i].HostPath == runtimeFiles[j].HostPath {
			return runtimeFiles[i].SourcePath < runtimeFiles[j].SourcePath
		}
		return runtimeFiles[i].HostPath < runtimeFiles[j].HostPath
	})

	payload, err := json.Marshal(desiredStateFingerprint{Config: cfg, RuntimeFiles: runtimeFiles})
	if err != nil {
		return "", fmt.Errorf("marshal desired state fingerprint: %w", err)
	}

	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func deferredCheckpointWarnings(completedCheckpoints []string) []string {
	warnings := make([]string, 0, 2)
	if containsCompletedCheckpoint(completedCheckpoints, deployCheckpointLegoCommandDeferred) {
		if containsCompletedCheckpoint(completedCheckpoints, deployCheckpointNginxActivated) {
			warnings = append(warnings, "lego is not installed; public certificate issuance was deferred and Nginx remains on the temporary HTTP-01 bootstrap certificate")
		} else {
			warnings = append(warnings, "lego is not installed; certificate issuance and Nginx activation were deferred")
		}
	}
	if containsCompletedCheckpoint(completedCheckpoints, deployCheckpointSystemdDaemonReloadDeferred) {
		warnings = append(warnings, "systemd is unavailable; service enablement and onboarding were deferred")
	}
	return warnings
}

func containsCompletedCheckpoint(completedCheckpoints []string, want string) bool {
	trimmedWant := strings.TrimSpace(want)
	if trimmedWant == "" {
		return false
	}
	for _, checkpoint := range completedCheckpoints {
		if strings.TrimSpace(checkpoint) == trimmedWant {
			return true
		}
	}
	return false
}

func systemdCommandDeferred(result host.Result, err error) bool {
	if err == nil {
		return false
	}

	messages := []string{result.Stdout, result.Stderr, err.Error()}
	var commandErr *host.CommandError
	if errors.As(err, &commandErr) {
		messages = append(messages, commandErr.Result.Stdout, commandErr.Result.Stderr)
	}

	for _, message := range messages {
		if systemdUnavailableMessage(message) {
			return true
		}
	}
	return false
}

func systemdUnavailableMessage(message string) bool {
	text := strings.ToLower(strings.TrimSpace(message))
	if text == "" {
		return false
	}
	return strings.Contains(text, "system has not been booted with systemd") ||
		strings.Contains(text, "failed to connect to bus: no such file or directory")
}

func probeURL(client *http.Client, rawURL string, method string) (int, string, error) {
	request, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return 0, rawURL, err
	}
	request.Header.Set("User-Agent", "meshify-preflight/1.0")
	if method == http.MethodGet {
		request.Header.Set("Range", "bytes=0-0")
	}

	client = httpClientOrDefault(client, 5*time.Second)
	response, err := client.Do(request)
	if err != nil {
		return 0, rawURL, err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if method == http.MethodGet {
		_, _ = io.CopyN(io.Discard, response.Body, 1)
	}
	return response.StatusCode, response.Request.URL.String(), nil
}

func appendProbeDetail(existing string, addition string) string {
	existing = strings.TrimSpace(existing)
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return existing
	}
	if existing == "" {
		return addition
	}
	return existing + " " + addition
}

func commandProbeDetail(err error, output string) string {
	output = strings.TrimSpace(output)
	switch {
	case output != "" && err != nil:
		return fmt.Sprintf("%s (%v)", output, err)
	case output != "":
		return output
	case err != nil:
		return err.Error()
	default:
		return ""
	}
}

func splitLabeledPort(value string) (int, string, bool) {
	portText, protocol, ok := strings.Cut(value, "/")
	if !ok {
		return 0, "", false
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil {
		return 0, "", false
	}
	return port, strings.ToLower(strings.TrimSpace(protocol)), true
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func parseServerHost(raw string) string {
	parsedURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsedURL.Hostname())
}
