package tls

import (
	"fmt"
	"strings"

	"meshify/internal/acme"
	legocomponent "meshify/internal/components/lego"
	"meshify/internal/config"
)

const (
	LegoBinaryPath = legocomponent.BinaryPath
	LegoDataPath   = "/var/lib/meshify/lego"
	WebrootPath    = "/var/lib/meshify/acme-challenges"
	RunHookPath    = "/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh"
	RenewService   = "meshify-lego-renew.service"
	RenewTimer     = "meshify-lego-renew.timer"
)

type ChallengePlan struct {
	Challenge string
	Provider  string
	EnvFile   string
	Webroot   string
}

func NewChallengePlan(cfg config.Config) (ChallengePlan, error) {
	if err := cfg.Validate(); err != nil {
		return ChallengePlan{}, err
	}
	switch strings.TrimSpace(cfg.Default.ACMEChallenge) {
	case config.ACMEChallengeHTTP01:
		return ChallengePlan{Challenge: config.ACMEChallengeHTTP01, Webroot: WebrootPath}, nil
	case config.ACMEChallengeDNS01:
		provider, err := acme.CanonicalDNSProvider(cfg.Advanced.DNS01.Provider)
		if err != nil {
			return ChallengePlan{}, err
		}
		return ChallengePlan{
			Challenge: config.ACMEChallengeDNS01,
			Provider:  provider,
			EnvFile:   strings.TrimSpace(cfg.Advanced.DNS01.EnvFile),
		}, nil
	default:
		return ChallengePlan{}, fmt.Errorf("unsupported ACME challenge %q", cfg.Default.ACMEChallenge)
	}
}

func StableTLSDir(serverName string) string {
	return "/etc/meshify/tls/" + strings.TrimSpace(serverName)
}

func StableFullchainPath(serverName string) string {
	return StableTLSDir(serverName) + "/fullchain.pem"
}

func StablePrivateKeyPath(serverName string) string {
	return StableTLSDir(serverName) + "/privkey.pem"
}

type DNSProviderInfo = acme.DNSProviderInfo

func SupportedDNSProviders() []DNSProviderInfo {
	return acme.SupportedDNSProviders()
}

func SupportedDNSProviderNames() string {
	return acme.SupportedDNSProviderNames()
}

func CanonicalDNSProvider(provider string) (string, error) {
	return acme.CanonicalDNSProvider(provider)
}

func DNSProvider(provider string) (DNSProviderInfo, error) {
	return acme.DNSProvider(provider)
}
