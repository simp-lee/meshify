package acme

import (
	"strings"
	"testing"
)

func TestDNSProviderCanonicalizesLegoCodes(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"cloudflare":   "cloudflare",
		"route53":      "route53",
		"digitalocean": "digitalocean",
		"google":       "gcloud",
		"gcloud":       "gcloud",
		"  GCLOUD  ":   "gcloud",
	}

	for provider, want := range tests {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalDNSProvider(provider)
			if err != nil {
				t.Fatalf("CanonicalDNSProvider() error = %v", err)
			}
			if got != want {
				t.Fatalf("CanonicalDNSProvider() = %q, want %q", got, want)
			}
		})
	}
}

func TestDNSProviderRejectsLegacyPluginAliases(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"dns-cloudflare", "cf", "aws", "do", "gce"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			_, err := CanonicalDNSProvider(provider)
			if err == nil {
				t.Fatal("CanonicalDNSProvider() error = nil, want unsupported legacy-style alias")
			}
			if !strings.Contains(err.Error(), "unsupported DNS-01 provider") {
				t.Fatalf("CanonicalDNSProvider() error = %q, want unsupported provider failure", err.Error())
			}
		})
	}
}

func TestValidateDNSProviderEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prov    string
		env     map[string]string
		wantErr string
	}{
		{
			name: "cloudflare token file",
			prov: "cloudflare",
			env:  map[string]string{"CF_DNS_API_TOKEN_FILE": "/run/secrets/cf-token"},
		},
		{
			name: "cloudflare global key pair",
			prov: "cloudflare",
			env:  map[string]string{"CF_API_EMAIL": "ops@example.com", "CF_API_KEY_FILE": "/run/secrets/cf-key"},
		},
		{
			name: "cloudflare mixed email and api key aliases",
			prov: "cloudflare",
			env:  map[string]string{"CF_API_EMAIL": "ops@example.com", "CLOUDFLARE_API_KEY_FILE": "/run/secrets/cf-key"},
		},
		{
			name: "cloudflare dns token with mixed optional zone token alias",
			prov: "cloudflare",
			env:  map[string]string{"CF_DNS_API_TOKEN_FILE": "/run/secrets/cf-token", "CLOUDFLARE_ZONE_API_TOKEN_FILE": "/run/secrets/cf-zone-token"},
		},
		{
			name:    "cloudflare raw token rejected for systemd renewal",
			prov:    "cloudflare",
			env:     map[string]string{"CF_DNS_API_TOKEN": "token"},
			wantErr: "CF_DNS_API_TOKEN must not be set directly",
		},
		{
			name:    "cloudflare missing credentials",
			prov:    "cloudflare",
			env:     map[string]string{"CF_API_EMAIL": "ops@example.com"},
			wantErr: "env_file for DNS provider cloudflare must contain one of",
		},
		{
			name:    "cloudflare lowercase token name",
			prov:    "cloudflare",
			env:     map[string]string{"cf_dns_api_token": "token"},
			wantErr: "must use exact uppercase lego variable name CF_DNS_API_TOKEN",
		},
		{
			name: "digitalocean token",
			prov: "digitalocean",
			env:  map[string]string{"DO_AUTH_TOKEN_FILE": "/run/secrets/do-token"},
		},
		{
			name: "route53 shared credentials",
			prov: "route53",
			env:  map[string]string{"AWS_SHARED_CREDENTIALS_FILE": "/root/.aws/credentials"},
		},
		{
			name: "route53 hosted zone with ambient credentials",
			prov: "route53",
			env:  map[string]string{"AWS_HOSTED_ZONE_ID": "Z1234567890"},
		},
		{
			name: "route53 hosted zone file with ambient credentials",
			prov: "route53",
			env:  map[string]string{"AWS_HOSTED_ZONE_ID_FILE": "/run/secrets/aws-zone"},
		},
		{
			name: "route53 config file with ambient credentials",
			prov: "route53",
			env:  map[string]string{"AWS_CONFIG_FILE": "/root/.aws/config", "AWS_PROFILE": "meshify"},
		},
		{
			name:    "route53 key pair rejected for systemd renewal",
			prov:    "route53",
			env:     map[string]string{"AWS_ACCESS_KEY_ID": "key", "AWS_SECRET_ACCESS_KEY": "secret"},
			wantErr: "AWS_ACCESS_KEY_ID must not be set directly",
		},
		{
			name:    "route53 empty env file",
			prov:    "route53",
			env:     nil,
			wantErr: "env_file for DNS provider route53 must contain a supported lego environment variable",
		},
		{
			name:    "route53 unsupported key file suffix",
			prov:    "route53",
			env:     map[string]string{"AWS_ACCESS_KEY_ID_FILE": "/run/secrets/aws-key", "AWS_SECRET_ACCESS_KEY_FILE": "/run/secrets/aws-secret"},
			wantErr: "AWS_ACCESS_KEY_ID_FILE is not supported",
		},
		{
			name: "gcloud project with service account file",
			prov: "google",
			env:  map[string]string{"GCE_PROJECT": "project-id", "GCE_SERVICE_ACCOUNT_FILE": "/root/gcloud.json"},
		},
		{
			name: "gcloud service account file with embedded project",
			prov: "google",
			env:  map[string]string{"GCE_SERVICE_ACCOUNT_FILE": "/root/gcloud.json"},
		},
		{
			name: "gcloud project with ambient application default credentials",
			prov: "gcloud",
			env:  map[string]string{"GCE_PROJECT": "project-id"},
		},
		{
			name: "gcloud project file with ambient application default credentials",
			prov: "gcloud",
			env:  map[string]string{"GCE_PROJECT_FILE": "/run/secrets/gcloud-project"},
		},
		{
			name: "gcloud project with application credentials",
			prov: "gcloud",
			env:  map[string]string{"GCE_PROJECT": "project-id", "GOOGLE_APPLICATION_CREDENTIALS": "/root/gcloud.json"},
		},
		{
			name: "gcloud project with impersonation",
			prov: "gcloud",
			env:  map[string]string{"GCE_PROJECT": "project-id", "GCE_IMPERSONATE_SERVICE_ACCOUNT": "target-sa@project-id.iam.gserviceaccount.com"},
		},
		{
			name: "gcloud application credentials with metadata project",
			prov: "gcloud",
			env:  map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/root/gcloud.json"},
		},
		{
			name:    "gcloud synthetic application credentials file suffix",
			prov:    "gcloud",
			env:     map[string]string{"GCE_PROJECT": "project-id", "GOOGLE_APPLICATION_CREDENTIALS_FILE": "/root/gcloud.json"},
			wantErr: "GOOGLE_APPLICATION_CREDENTIALS_FILE is not supported",
		},
		{
			name: "gcloud zone id with ambient credentials",
			prov: "gcloud",
			env:  map[string]string{"GCE_ZONE_ID": "meshify-zone"},
		},
		{
			name: "gcloud zone id file with ambient credentials",
			prov: "gcloud",
			env:  map[string]string{"GCE_ZONE_ID_FILE": "/run/secrets/gcloud-zone"},
		},
		{
			name:    "gcloud empty env file",
			prov:    "gcloud",
			env:     nil,
			wantErr: "env_file for DNS provider gcloud must contain a supported lego environment variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateDNSProviderEnvironment(tt.prov, tt.env)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateDNSProviderEnvironment() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateDNSProviderEnvironment() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateDNSProviderEnvironment() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSupportedDNSProviderEnvFileVarsIncludesOfficialFileRefs(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"cloudflare":   {"CF_DNS_API_TOKEN_FILE", "CF_ZONE_API_TOKEN_FILE", "CLOUDFLARE_API_KEY_FILE", "CLOUDFLARE_ZONE_API_TOKEN_FILE"},
		"digitalocean": {"DO_AUTH_TOKEN_FILE"},
		"gcloud":       {"GCE_SERVICE_ACCOUNT_FILE", "GCE_IMPERSONATE_SERVICE_ACCOUNT_FILE"},
		"route53":      {"AWS_CONFIG_FILE", "AWS_HOSTED_ZONE_ID_FILE", "AWS_SHARED_CREDENTIALS_FILE"},
	}
	for provider, wants := range tests {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			got, err := SupportedDNSProviderEnvFileVars(provider)
			if err != nil {
				t.Fatalf("SupportedDNSProviderEnvFileVars() error = %v", err)
			}
			for _, want := range wants {
				if !containsString(got, want) {
					t.Fatalf("SupportedDNSProviderEnvFileVars(%q) = %#v, want %q", provider, got, want)
				}
			}
		})
	}

	route53, err := SupportedDNSProviderEnvFileVars("route53")
	if err != nil {
		t.Fatalf("SupportedDNSProviderEnvFileVars(route53) error = %v", err)
	}
	for _, unsupported := range []string{"AWS_ACCESS_KEY_ID_FILE", "AWS_SECRET_ACCESS_KEY_FILE"} {
		if containsString(route53, unsupported) {
			t.Fatalf("SupportedDNSProviderEnvFileVars(route53) = %#v, must not include unsupported %q", route53, unsupported)
		}
	}

	gcloud, err := SupportedDNSProviderEnvFileVars("gcloud")
	if err != nil {
		t.Fatalf("SupportedDNSProviderEnvFileVars(gcloud) error = %v", err)
	}
	if containsString(gcloud, "GOOGLE_APPLICATION_CREDENTIALS_FILE") {
		t.Fatalf("SupportedDNSProviderEnvFileVars(gcloud) = %#v, must not include unsupported GOOGLE_APPLICATION_CREDENTIALS_FILE", gcloud)
	}
}

func TestSupportedDNSProvidersReturnsCopies(t *testing.T) {
	t.Parallel()

	providers := SupportedDNSProviders()
	providers[0].Alias[0] = "mutated"
	providers[0].RequiredEnvSets[0][0] = "MUTATED"

	again := SupportedDNSProviders()
	if again[0].Alias[0] == "mutated" {
		t.Fatal("SupportedDNSProviders() leaked aliases backing array")
	}
	if again[0].RequiredEnvSets[0][0] == "MUTATED" {
		t.Fatal("SupportedDNSProviders() leaked required env set backing array")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
