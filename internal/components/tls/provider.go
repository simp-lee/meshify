package tls

import (
	"fmt"
	"meshify/internal/config"
	"slices"
	"strings"
)

const (
	WebrootPath    = "/var/www/certbot"
	DeployHookPath = "/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh"
)

type ChallengePlan struct {
	Challenge string
	Provider  string
	Zone      string
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
		provider, err := CanonicalDNSProvider(cfg.Advanced.DNS01.Provider)
		if err != nil {
			return ChallengePlan{}, err
		}
		return ChallengePlan{
			Challenge: config.ACMEChallengeDNS01,
			Provider:  provider,
			Zone:      strings.TrimSpace(cfg.Advanced.DNS01.Zone),
		}, nil
	default:
		return ChallengePlan{}, fmt.Errorf("unsupported ACME challenge %q", cfg.Default.ACMEChallenge)
	}
}

type DNSProviderInfo struct {
	Name   string
	Plugin string
	Alias  []string
}

var supportedDNSProviders = []DNSProviderInfo{
	{Name: "cloudflare", Plugin: "dns-cloudflare", Alias: []string{"cloudflare", "dns-cloudflare"}},
	{Name: "route53", Plugin: "dns-route53", Alias: []string{"route53", "aws", "dns-route53"}},
	{Name: "digitalocean", Plugin: "dns-digitalocean", Alias: []string{"digitalocean", "do", "dns-digitalocean"}},
	{Name: "google", Plugin: "dns-google", Alias: []string{"google", "gcloud", "gce", "dns-google"}},
	{Name: "azure", Plugin: "dns-azure", Alias: []string{"azure", "dns-azure"}},
}

func SupportedDNSProviders() []DNSProviderInfo {
	providers := make([]DNSProviderInfo, 0, len(supportedDNSProviders))
	for _, provider := range supportedDNSProviders {
		provider.Alias = append([]string(nil), provider.Alias...)
		providers = append(providers, provider)
	}
	return providers
}

func CanonicalDNSProvider(provider string) (string, error) {
	normalized := normalizeProviderAlias(provider)
	if normalized == "" {
		return "", fmt.Errorf("DNS-01 provider is required")
	}
	for _, supported := range supportedDNSProviders {
		if slices.Contains(supported.Alias, normalized) {
			return supported.Name, nil
		}
	}
	return "", fmt.Errorf("unsupported DNS-01 provider %q; supported providers: %s", strings.TrimSpace(provider), supportedDNSProviderNames())
}

func DNSPluginName(provider string) (string, error) {
	canonical, err := CanonicalDNSProvider(provider)
	if err != nil {
		return "", err
	}
	for _, supported := range supportedDNSProviders {
		if supported.Name == canonical {
			return supported.Plugin, nil
		}
	}
	return "", fmt.Errorf("unsupported DNS-01 provider %q; supported providers: %s", strings.TrimSpace(provider), supportedDNSProviderNames())
}

func normalizeProviderAlias(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	provider = strings.ReplaceAll(provider, "_", "-")
	provider = strings.Join(strings.Fields(provider), "-")
	return provider
}

func supportedDNSProviderNames() string {
	names := make([]string, 0, len(supportedDNSProviders))
	for _, supported := range supportedDNSProviders {
		names = append(names, supported.Name)
	}
	return strings.Join(names, ", ")
}
