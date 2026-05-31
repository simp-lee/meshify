package appsvc

import (
	"fmt"
	"hash/fnv"
	"meshify/internal/appconfig"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	LegoBinaryPath     = "/opt/meshify/bin/lego"
	GoAccessBinaryPath = "/usr/bin/goaccess"

	linuxUserNameMaxLen = 32
)

type Names struct {
	AppName            string
	VarPrefix          string
	SystemUser         string
	SystemGroup        string
	ServiceUnit        string
	RenewServiceUnit   string
	RenewTimerUnit     string
	VarLibDir          string
	EtcDir             string
	HookDir            string
	NginxAvailablePath string
	NginxEnabledPath   string
	WebrootPath        string
	LegoDataPath       string
	TLSDir             string
	VarLibMarkerPath   string
	EtcMarkerPath      string
	HookDirMarkerPath  string
	FullchainPath      string
	PrivateKeyPath     string
	TLSMarkerPath      string
	HookPath           string

	GoAccessSystemUser                 string
	GoAccessSystemGroup                string
	GoAccessServiceUnit                string
	GoAccessConfigPath                 string
	GoAccessReportDir                  string
	GoAccessReportPath                 string
	GoAccessDBPath                     string
	GoAccessLogDir                     string
	GoAccessLogDirMarkerPath           string
	GoAccessCanonicalAccessLogPath     string
	GoAccessLogrotatePath              string
	GoAccessSuggestedAuthBasicUserFile string
	GoAccessWebSocketHost              string
	GoAccessWebSocketPort              int
	GoAccessWebSocketListen            string
	GoAccessNginxLogFormatName         string
}

func NewNames(cfg appconfig.Config) (Names, error) {
	if err := cfg.Validate(); err != nil {
		return Names{}, err
	}
	appName := cfg.ResourceName()
	primaryDomain := cfg.PrimaryDomain()
	if appName == "" || primaryDomain == "" {
		return Names{}, fmt.Errorf("app name and primary domain are required")
	}

	varLibDir := filepath.Join("/var/lib", appName)
	etcDir := filepath.Join("/etc", appName)
	hookDir := filepath.Join("/usr/local/lib/meshify/apps", appName)
	tlsDir := filepath.Join(etcDir, "tls", primaryDomain)
	goAccessReportDir := filepath.Join(varLibDir, "goaccess")
	goAccessLogDir := filepath.Join("/var/log/meshify/apps", appName)
	goAccessWebSocketHost, goAccessWebSocketPort, err := goAccessWebSocketListenParts(cfg)
	if err != nil {
		return Names{}, err
	}
	return Names{
		AppName:            appName,
		VarPrefix:          strings.ReplaceAll(appName, "-", "_"),
		SystemUser:         appName,
		SystemGroup:        appName,
		ServiceUnit:        appName + ".service",
		RenewServiceUnit:   appName + "-lego-renew.service",
		RenewTimerUnit:     appName + "-lego-renew.timer",
		VarLibDir:          varLibDir,
		EtcDir:             etcDir,
		HookDir:            hookDir,
		NginxAvailablePath: filepath.Join("/etc/nginx/sites-available", appName+".conf"),
		NginxEnabledPath:   filepath.Join("/etc/nginx/sites-enabled", appName+".conf"),
		WebrootPath:        filepath.Join(varLibDir, "acme-challenges"),
		LegoDataPath:       filepath.Join(varLibDir, "lego"),
		TLSDir:             tlsDir,
		VarLibMarkerPath:   filepath.Join(varLibDir, ".meshify-managed"),
		EtcMarkerPath:      filepath.Join(etcDir, ".meshify-managed"),
		HookDirMarkerPath:  filepath.Join(hookDir, ".meshify-managed"),
		FullchainPath:      filepath.Join(tlsDir, "fullchain.pem"),
		PrivateKeyPath:     filepath.Join(tlsDir, "privkey.pem"),
		TLSMarkerPath:      filepath.Join(tlsDir, ".meshify-managed"),
		HookPath:           filepath.Join(hookDir, "install-cert-and-reload-nginx.sh"),

		GoAccessSystemUser:                 goAccessIdentityName(appName),
		GoAccessSystemGroup:                goAccessIdentityName(appName),
		GoAccessServiceUnit:                appName + "-goaccess.service",
		GoAccessConfigPath:                 filepath.Join(etcDir, "goaccess.conf"),
		GoAccessReportDir:                  goAccessReportDir,
		GoAccessReportPath:                 filepath.Join(goAccessReportDir, "report.html"),
		GoAccessDBPath:                     filepath.Join(goAccessReportDir, "db"),
		GoAccessLogDir:                     goAccessLogDir,
		GoAccessLogDirMarkerPath:           filepath.Join(goAccessLogDir, ".meshify-managed"),
		GoAccessCanonicalAccessLogPath:     GoAccessCanonicalAccessLogPath(cfg),
		GoAccessLogrotatePath:              filepath.Join("/etc/logrotate.d", appName+"-goaccess"),
		GoAccessSuggestedAuthBasicUserFile: filepath.Join(etcDir, "goaccess.htpasswd"),
		GoAccessWebSocketHost:              goAccessWebSocketHost,
		GoAccessWebSocketPort:              goAccessWebSocketPort,
		GoAccessWebSocketListen:            net.JoinHostPort(goAccessWebSocketHost, strconv.Itoa(goAccessWebSocketPort)),
		GoAccessNginxLogFormatName:         "meshify_app_" + strings.ReplaceAll(appName, "-", "_") + "_enhanced",
	}, nil
}

func GoAccessCanonicalAccessLogPath(cfg appconfig.Config) string {
	return cfg.NginxGoAccessCanonicalAccessLogPath()
}

func GoAccessManagesCanonicalAccessLog(cfg appconfig.Config) bool {
	return cfg.NginxGoAccessManagesCanonicalAccessLog()
}

func goAccessWebSocketListenParts(cfg appconfig.Config) (string, int, error) {
	listen := appconfig.EffectiveNginxGoAccessWebSocketListen(cfg.ResourceName(), cfg.Nginx.GoAccess)
	host, portString, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("parse GoAccess websocket listen %q: %w", listen, err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		return "", 0, fmt.Errorf("parse GoAccess websocket listen port %q: %w", portString, err)
	}
	return host, port, nil
}

func goAccessIdentityName(appName string) string {
	const prefix = "meshify-goaccess-"
	if len(prefix)+len(appName) <= linuxUserNameMaxLen {
		return prefix + appName
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(appName))
	suffix := fmt.Sprintf("%08x", hash.Sum32())
	const shortPrefix = "mga-"
	headLen := linuxUserNameMaxLen - len(shortPrefix) - len("-") - len(suffix)
	if headLen > len(appName) {
		headLen = len(appName)
	}
	return shortPrefix + appName[:headLen] + "-" + suffix
}
