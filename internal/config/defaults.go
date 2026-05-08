package config

func New() Config {
	return Config{
		APIVersion: APIVersion,
		Default: DefaultConfig{
			ACMEChallenge: ACMEChallengeHTTP01,
		},
		Advanced: AdvancedConfig{
			HeadscaleSource: HeadscaleSourceConfig{
				Mode:    PackageSourceModeDirect,
				Version: DefaultHeadscaleVersion,
			},
			LegoSource: LegoSourceConfig{
				Mode: PackageSourceModeDirect,
			},
			Platform: PlatformConfig{
				Arch: ArchAMD64,
			},
		},
	}
}

func ExampleConfig() Config {
	cfg := New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}
