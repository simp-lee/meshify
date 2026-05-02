package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func (c Config) ExportYAML() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal config yaml: %w", err)
	}

	return data, nil
}

func (c Config) WriteFile(path string) error {
	data, err := c.ExportYAML()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	return nil
}

func ExampleYAML() ([]byte, error) {
	if err := ExampleConfig().Validate(); err != nil {
		return nil, err
	}

	data := fmt.Sprintf(`api_version: %s

# Edit the default section first. It is the only part that should matter for the
# first successful deployment.
default:
  server_url: "https://hs.example.com"
  # base_domain must differ from the host part of server_url and cannot be its
  # parent domain.
  base_domain: "tailnet.example.com"
  certificate_email: "ops@example.com"
  # Keep HTTP-01 unless your environment requires DNS-01.
  acme_challenge: "http-01"

# The advanced section is opt-in. Leave values empty unless you have a real
# need for mirrors, offline packages, proxies, DNS-01, or public IP overrides.
advanced:
  package_source:
    mode: "direct" # direct | mirror | offline
    version: "%s"
    url: ""
    sha256: ""
    file_path: ""

  proxy:
    http_proxy: ""
    https_proxy: ""
    no_proxy: ""

  dns01:
    provider: ""
    zone: ""
    # Supply provider credentials through the host environment, not this file.

  network:
    public_ipv4: ""
    # Optional enhancement. Leave empty when the host has no usable IPv6.
    public_ipv6: ""

  platform:
    arch: "%s"
`, APIVersion, DefaultHeadscaleVersion, ArchAMD64)

	return []byte(data), nil
}

func WriteExampleFile(path string) error {
	data, err := ExampleYAML()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create example config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write example config file: %w", err)
	}

	return nil
}
