package headscale

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"meshify/internal/config"
	"net"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ListenAddress        = "127.0.0.1:8080"
	MetricsListenAddress = "127.0.0.1:9090"
	GRPCListenAddress    = "127.0.0.1:50443"
	STUNListenAddress    = "0.0.0.0:3478"
	UnixSocketPath       = "/var/run/headscale/headscale.sock"
	UnixSocketPermission = "0770"
)

type RuntimeConfig struct {
	ServerURL            string       `yaml:"server_url"`
	ListenAddr           string       `yaml:"listen_addr"`
	MetricsListenAddr    string       `yaml:"metrics_listen_addr"`
	GRPCListenAddr       string       `yaml:"grpc_listen_addr"`
	GRPCAllowInsecure    bool         `yaml:"grpc_allow_insecure"`
	DERP                 DERPConfig   `yaml:"derp"`
	DisableCheckUpdates  bool         `yaml:"disable_check_updates"`
	Policy               PolicyConfig `yaml:"policy"`
	DNS                  DNSConfig    `yaml:"dns"`
	UnixSocket           string       `yaml:"unix_socket"`
	UnixSocketPermission string       `yaml:"unix_socket_permission"`
	Logtail              ToggleConfig `yaml:"logtail"`
}

type DERPConfig struct {
	Server            DERPServerConfig `yaml:"server"`
	URLs              []string         `yaml:"urls"`
	Paths             []string         `yaml:"paths"`
	AutoUpdateEnabled bool             `yaml:"auto_update_enabled"`
}

type DERPServerConfig struct {
	Enabled                            bool   `yaml:"enabled"`
	RegionID                           int    `yaml:"region_id"`
	RegionCode                         string `yaml:"region_code"`
	RegionName                         string `yaml:"region_name"`
	VerifyClients                      bool   `yaml:"verify_clients"`
	STUNListenAddr                     string `yaml:"stun_listen_addr"`
	PrivateKeyPath                     string `yaml:"private_key_path"`
	AutomaticallyAddEmbeddedDERPRegion bool   `yaml:"automatically_add_embedded_derp_region"`
	IPv4                               string `yaml:"ipv4"`
	IPv6                               string `yaml:"ipv6"`
}

type PolicyConfig struct {
	Mode string `yaml:"mode"`
	Path string `yaml:"path"`
}

type DNSConfig struct {
	MagicDNS         bool   `yaml:"magic_dns"`
	BaseDomain       string `yaml:"base_domain"`
	OverrideLocalDNS bool   `yaml:"override_local_dns"`
}

type ToggleConfig struct {
	Enabled bool `yaml:"enabled"`
}

func ParseRuntimeConfig(data []byte) (RuntimeConfig, error) {
	var runtimeConfig RuntimeConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&runtimeConfig); err != nil {
		return RuntimeConfig{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return RuntimeConfig{}, fmt.Errorf("headscale runtime config must contain exactly one YAML document")
	} else if err != io.EOF {
		return RuntimeConfig{}, err
	}
	return runtimeConfig, nil
}

func ValidateRuntimeConfig(cfg config.Config, data []byte) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	runtimeConfig, err := ParseRuntimeConfig(data)
	if err != nil {
		return err
	}

	var errs []string
	expectEqual(&errs, "server_url", runtimeConfig.ServerURL, strings.TrimSpace(cfg.Default.ServerURL))
	expectEqual(&errs, "listen_addr", runtimeConfig.ListenAddr, ListenAddress)
	expectLoopback(&errs, "metrics_listen_addr", runtimeConfig.MetricsListenAddr)
	expectEqual(&errs, "grpc_listen_addr", runtimeConfig.GRPCListenAddr, GRPCListenAddress)
	if runtimeConfig.GRPCAllowInsecure {
		errs = append(errs, "grpc_allow_insecure must be false")
	}

	if !runtimeConfig.DERP.Server.Enabled {
		errs = append(errs, "derp.server.enabled must be true")
	}
	if !runtimeConfig.DERP.Server.VerifyClients {
		errs = append(errs, "derp.server.verify_clients must be true")
	}
	if !runtimeConfig.DERP.Server.AutomaticallyAddEmbeddedDERPRegion {
		errs = append(errs, "derp.server.automatically_add_embedded_derp_region must be true")
	}
	expectEqual(&errs, "derp.server.stun_listen_addr", runtimeConfig.DERP.Server.STUNListenAddr, STUNListenAddress)
	if len(runtimeConfig.DERP.URLs) != 0 {
		errs = append(errs, "derp.urls must be empty for DERP-only deployment")
	}
	if len(runtimeConfig.DERP.Paths) != 0 {
		errs = append(errs, "derp.paths must be empty for the default embedded DERP deployment")
	}
	if runtimeConfig.DERP.AutoUpdateEnabled {
		errs = append(errs, "derp.auto_update_enabled must be false")
	}

	if !runtimeConfig.DisableCheckUpdates {
		errs = append(errs, "disable_check_updates must be true")
	}
	if runtimeConfig.Logtail.Enabled {
		errs = append(errs, "logtail.enabled must be false")
	}
	expectEqual(&errs, "policy.mode", runtimeConfig.Policy.Mode, "file")
	expectEqual(&errs, "policy.path", runtimeConfig.Policy.Path, PolicyPath)
	if !runtimeConfig.DNS.MagicDNS {
		errs = append(errs, "dns.magic_dns must be true")
	}
	expectEqual(&errs, "dns.base_domain", runtimeConfig.DNS.BaseDomain, strings.TrimSpace(cfg.Default.BaseDomain))
	if !runtimeConfig.DNS.OverrideLocalDNS {
		errs = append(errs, "dns.override_local_dns must be true")
	}
	expectEqual(&errs, "unix_socket", runtimeConfig.UnixSocket, UnixSocketPath)
	expectEqual(&errs, "unix_socket_permission", runtimeConfig.UnixSocketPermission, UnixSocketPermission)

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func expectEqual(errs *[]string, field string, got string, want string) {
	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		*errs = append(*errs, fmt.Sprintf("%s must be %q", field, want))
	}
}

func expectLoopback(errs *[]string, field string, value string) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s must be a host:port loopback listener", field))
		return
	}
	parsedIP := net.ParseIP(host)
	if host == "localhost" || parsedIP != nil && parsedIP.IsLoopback() {
		return
	}
	*errs = append(*errs, fmt.Sprintf("%s must listen on loopback", field))
}
