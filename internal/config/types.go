package config

const (
	APIVersion              = "meshify/v1alpha1"
	DefaultHeadscaleVersion = "0.28.0"

	ACMEChallengeHTTP01 = "http-01"
	ACMEChallengeDNS01  = "dns-01"

	PackageSourceModeDirect  = "direct"
	PackageSourceModeMirror  = "mirror"
	PackageSourceModeOffline = "offline"

	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

type Config struct {
	APIVersion string         `yaml:"api_version"`
	Default    DefaultConfig  `yaml:"default"`
	Advanced   AdvancedConfig `yaml:"advanced"`
}

type DefaultConfig struct {
	ServerURL        string `yaml:"server_url"`
	BaseDomain       string `yaml:"base_domain"`
	CertificateEmail string `yaml:"certificate_email"`
	ACMEChallenge    string `yaml:"acme_challenge"`
}

type AdvancedConfig struct {
	PackageSource PackageSourceConfig `yaml:"package_source"`
	Proxy         ProxyConfig         `yaml:"proxy"`
	DNS01         DNS01Config         `yaml:"dns01"`
	Network       NetworkConfig       `yaml:"network"`
	Platform      PlatformConfig      `yaml:"platform"`
}

type PackageSourceConfig struct {
	Mode     string `yaml:"mode"`
	Version  string `yaml:"version"`
	URL      string `yaml:"url"`
	SHA256   string `yaml:"sha256"`
	FilePath string `yaml:"file_path"`
}

type ProxyConfig struct {
	HTTPProxy  string `yaml:"http_proxy"`
	HTTPSProxy string `yaml:"https_proxy"`
	NoProxy    string `yaml:"no_proxy"`
}

type DNS01Config struct {
	Provider string `yaml:"provider"`
	Zone     string `yaml:"zone"`
}

type NetworkConfig struct {
	PublicIPv4 string `yaml:"public_ipv4"`
	PublicIPv6 string `yaml:"public_ipv6"`
}

type PlatformConfig struct {
	Arch string `yaml:"arch"`
}
