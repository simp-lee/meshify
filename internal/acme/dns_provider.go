package acme

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type DNSProviderInfo struct {
	Name                        string
	LegoCode                    string
	Alias                       []string
	EnvFileRequired             bool
	AmbientCredentialsSupported bool
	RequiredEnvSets             [][]string
	OptionalEnvVars             []string
	UnsupportedEnvVars          []string
	RawSecretEnvVars            []string
}

var supportedDNSProviders = []DNSProviderInfo{
	{
		Name:            "cloudflare",
		LegoCode:        "cloudflare",
		Alias:           []string{"cloudflare"},
		EnvFileRequired: true,
		RequiredEnvSets: [][]string{
			{"CF_DNS_API_TOKEN_FILE"},
			{"CLOUDFLARE_DNS_API_TOKEN_FILE"},
			{"CF_API_EMAIL", "CF_API_KEY_FILE"},
			{"CF_API_EMAIL", "CLOUDFLARE_API_KEY_FILE"},
			{"CLOUDFLARE_EMAIL", "CF_API_KEY_FILE"},
			{"CLOUDFLARE_EMAIL", "CLOUDFLARE_API_KEY_FILE"},
		},
		OptionalEnvVars: []string{
			"CLOUDFLARE_BASE_URL",
			"CLOUDFLARE_HTTP_TIMEOUT",
			"CLOUDFLARE_POLLING_INTERVAL",
			"CLOUDFLARE_PROPAGATION_TIMEOUT",
			"CLOUDFLARE_TTL",
		},
		RawSecretEnvVars: []string{
			"CF_API_KEY",
			"CF_DNS_API_TOKEN",
			"CF_ZONE_API_TOKEN",
			"CLOUDFLARE_API_KEY",
			"CLOUDFLARE_DNS_API_TOKEN",
			"CLOUDFLARE_ZONE_API_TOKEN",
		},
	},
	{
		Name:                        "route53",
		LegoCode:                    "route53",
		Alias:                       []string{"route53"},
		AmbientCredentialsSupported: true,
		RequiredEnvSets: [][]string{
			{"AWS_SHARED_CREDENTIALS_FILE"},
		},
		OptionalEnvVars: []string{
			"AWS_ASSUME_ROLE_ARN",
			"AWS_CONFIG_FILE",
			"AWS_EXTERNAL_ID",
			"AWS_HOSTED_ZONE_ID",
			"AWS_MAX_RETRIES",
			"AWS_POLLING_INTERVAL",
			"AWS_PRIVATE_ZONE",
			"AWS_PROFILE",
			"AWS_PROPAGATION_TIMEOUT",
			"AWS_REGION",
			"AWS_SDK_LOAD_CONFIG",
			"AWS_SESSION_TOKEN",
			"AWS_TTL",
			"AWS_WAIT_FOR_RECORD_SETS_CHANGED",
		},
		RawSecretEnvVars: []string{
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_SESSION_TOKEN",
		},
		UnsupportedEnvVars: []string{
			"AWS_ACCESS_KEY_ID_FILE",
			"AWS_ASSUME_ROLE_ARN_FILE",
			"AWS_EXTERNAL_ID_FILE",
			"AWS_PROFILE_FILE",
			"AWS_REGION_FILE",
			"AWS_SDK_LOAD_CONFIG_FILE",
			"AWS_SECRET_ACCESS_KEY_FILE",
			"AWS_SESSION_TOKEN_FILE",
		},
	},
	{
		Name:            "digitalocean",
		LegoCode:        "digitalocean",
		Alias:           []string{"digitalocean"},
		EnvFileRequired: true,
		RequiredEnvSets: [][]string{
			{"DO_AUTH_TOKEN_FILE"},
		},
		OptionalEnvVars: []string{
			"DO_API_URL",
			"DO_HTTP_TIMEOUT",
			"DO_POLLING_INTERVAL",
			"DO_PROPAGATION_TIMEOUT",
			"DO_TTL",
		},
		RawSecretEnvVars: []string{"DO_AUTH_TOKEN"},
	},
	{
		Name:                        "gcloud",
		LegoCode:                    "gcloud",
		Alias:                       []string{"google", "gcloud"},
		AmbientCredentialsSupported: true,
		RequiredEnvSets: [][]string{
			{"GCE_SERVICE_ACCOUNT_FILE"},
			{"GCE_PROJECT"},
			{"GCE_PROJECT", "GOOGLE_APPLICATION_CREDENTIALS"},
			{"GCE_PROJECT", "GCE_IMPERSONATE_SERVICE_ACCOUNT"},
		},
		OptionalEnvVars: []string{
			"GCE_ALLOW_PRIVATE_ZONE",
			"GCE_IMPERSONATE_SERVICE_ACCOUNT",
			"GCE_POLLING_INTERVAL",
			"GCE_PROPAGATION_TIMEOUT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_TTL",
			"GCE_ZONE_ID",
		},
		RawSecretEnvVars: []string{"GCE_SERVICE_ACCOUNT"},
		UnsupportedEnvVars: []string{
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		},
	},
}

func SupportedDNSProviders() []DNSProviderInfo {
	providers := make([]DNSProviderInfo, 0, len(supportedDNSProviders))
	for _, provider := range supportedDNSProviders {
		provider = cloneDNSProvider(provider)
		providers = append(providers, provider)
	}
	return providers
}

func SupportedDNSProviderNames() string {
	names := make([]string, 0, len(supportedDNSProviders))
	for _, supported := range supportedDNSProviders {
		names = append(names, supported.Name)
	}
	return strings.Join(names, ", ")
}

func CanonicalDNSProvider(provider string) (string, error) {
	info, err := DNSProvider(provider)
	if err != nil {
		return "", err
	}
	return info.LegoCode, nil
}

func DNSProvider(provider string) (DNSProviderInfo, error) {
	normalized := normalizeProviderAlias(provider)
	if normalized == "" {
		return DNSProviderInfo{}, fmt.Errorf("DNS-01 provider is required")
	}
	for _, supported := range supportedDNSProviders {
		if slices.Contains(supported.Alias, normalized) {
			return cloneDNSProvider(supported), nil
		}
	}
	return DNSProviderInfo{}, fmt.Errorf("unsupported DNS-01 provider %q; supported providers: %s", strings.TrimSpace(provider), SupportedDNSProviderNames())
}

func LegoDNSProviderCode(provider string) (string, error) {
	info, err := DNSProvider(provider)
	if err != nil {
		return "", err
	}
	return info.LegoCode, nil
}

func ValidateDNSProviderEnvironment(provider string, env map[string]string) error {
	info, err := DNSProvider(provider)
	if err != nil {
		return err
	}

	keys := normalizeEnvKeys(env)
	if got, want, ok := envKeyCaseMismatch(keys, info); ok {
		return fmt.Errorf("environment variable %s must use exact uppercase lego variable name %s", got, want)
	}
	for _, unsupported := range info.UnsupportedEnvVars {
		if _, ok := keys[unsupported]; ok {
			return fmt.Errorf("environment variable %s is not supported by lego DNS provider %s", unsupported, info.LegoCode)
		}
	}
	for _, secret := range info.RawSecretEnvVars {
		if _, ok := keys[secret]; ok {
			return fmt.Errorf("environment variable %s must not be set directly in advanced.dns01.env_file; use a file reference supported by lego so systemd EnvironmentFile does not carry raw DNS secrets", secret)
		}
	}

	for _, required := range info.RequiredEnvSets {
		if envSetSatisfied(required, keys) {
			return nil
		}
	}
	if info.AmbientCredentialsSupported && envContainsKnownProviderKey(keys, info) {
		return nil
	}

	if info.AmbientCredentialsSupported {
		return fmt.Errorf("env_file for DNS provider %s must contain a supported lego environment variable: %s", info.LegoCode, info.supportedEnvSummary())
	}
	return fmt.Errorf("env_file for DNS provider %s must contain one of: %s", info.LegoCode, info.requiredEnvSummary())
}

func SupportedDNSProviderEnvFileVars(provider string) ([]string, error) {
	info, err := DNSProvider(provider)
	if err != nil {
		return nil, err
	}

	unsupported := map[string]struct{}{}
	for _, key := range info.UnsupportedEnvVars {
		unsupported[key] = struct{}{}
	}

	seen := map[string]struct{}{}
	for _, key := range providerConfigEnvVars(info) {
		if strings.HasSuffix(key, "_FILE") {
			if _, blocked := unsupported[key]; !blocked {
				seen[key] = struct{}{}
			}
			continue
		}
		fileKey := key + "_FILE"
		if _, blocked := unsupported[fileKey]; !blocked {
			seen[fileKey] = struct{}{}
		}
	}
	for _, key := range info.RawSecretEnvVars {
		if strings.HasSuffix(key, "_FILE") {
			if _, blocked := unsupported[key]; !blocked {
				seen[key] = struct{}{}
			}
			continue
		}
		fileKey := key + "_FILE"
		if _, blocked := unsupported[fileKey]; !blocked {
			seen[fileKey] = struct{}{}
		}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func cloneDNSProvider(provider DNSProviderInfo) DNSProviderInfo {
	provider.Alias = append([]string(nil), provider.Alias...)
	provider.RequiredEnvSets = cloneStringMatrix(provider.RequiredEnvSets)
	provider.OptionalEnvVars = append([]string(nil), provider.OptionalEnvVars...)
	provider.UnsupportedEnvVars = append([]string(nil), provider.UnsupportedEnvVars...)
	provider.RawSecretEnvVars = append([]string(nil), provider.RawSecretEnvVars...)
	return provider
}

func cloneStringMatrix(values [][]string) [][]string {
	out := make([][]string, 0, len(values))
	for _, value := range values {
		out = append(out, append([]string(nil), value...))
	}
	return out
}

func (provider DNSProviderInfo) requiredEnvSummary() string {
	sets := make([]string, 0, len(provider.RequiredEnvSets))
	for _, required := range provider.RequiredEnvSets {
		sets = append(sets, strings.Join(required, "+"))
	}
	return strings.Join(sets, "; ")
}

func (provider DNSProviderInfo) supportedEnvSummary() string {
	keys := providerConfigEnvVars(provider)
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func normalizeEnvKeys(env map[string]string) map[string]struct{} {
	keys := make(map[string]struct{}, len(env))
	for key := range env {
		key = strings.TrimSpace(key)
		if key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys
}

func envKeyCaseMismatch(keys map[string]struct{}, provider DNSProviderInfo) (string, string, bool) {
	for key := range keys {
		for _, known := range knownEnvVars(provider) {
			if strings.EqualFold(key, known) && key != known {
				return key, known, true
			}
		}
	}
	return "", "", false
}

func knownEnvVars(provider DNSProviderInfo) []string {
	keys := providerConfigEnvVars(provider)
	for _, key := range providerConfigEnvVars(provider) {
		if !strings.HasSuffix(key, "_FILE") {
			keys = append(keys, key+"_FILE")
		}
	}
	keys = append(keys, provider.UnsupportedEnvVars...)
	keys = append(keys, provider.RawSecretEnvVars...)
	for _, key := range provider.RawSecretEnvVars {
		if !strings.HasSuffix(key, "_FILE") {
			keys = append(keys, key+"_FILE")
		}
	}
	return keys
}

func envContainsKnownProviderKey(keys map[string]struct{}, provider DNSProviderInfo) bool {
	for _, key := range knownEnvVars(provider) {
		if _, ok := keys[key]; ok {
			return true
		}
	}
	return false
}

func providerConfigEnvVars(provider DNSProviderInfo) []string {
	keys := []string{}
	for _, required := range provider.RequiredEnvSets {
		keys = append(keys, required...)
		for _, key := range required {
			if !strings.HasSuffix(key, "_FILE") {
				keys = append(keys, key+"_FILE")
			}
		}
	}
	keys = append(keys, provider.OptionalEnvVars...)
	return keys
}

func envSetSatisfied(required []string, keys map[string]struct{}) bool {
	for _, key := range required {
		if !envKeySatisfied(key, keys) {
			return false
		}
	}
	return true
}

func envKeySatisfied(key string, keys map[string]struct{}) bool {
	if _, ok := keys[key]; ok {
		return true
	}
	if strings.HasSuffix(key, "_FILE") {
		return false
	}
	_, ok := keys[key+"_FILE"]
	return ok
}

func normalizeProviderAlias(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	provider = strings.ReplaceAll(provider, "_", "-")
	provider = strings.Join(strings.Fields(provider), "-")
	return provider
}
