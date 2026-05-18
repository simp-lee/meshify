package appsvc

import (
	"fmt"
	"meshify/internal/appconfig"
	"path/filepath"
	"strings"
)

const (
	LegoBinaryPath = "/opt/meshify/bin/lego"
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
	}, nil
}
