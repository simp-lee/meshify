package appconfig

import (
	"hash/fnv"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

func New() Config {
	cfg := Config{APIVersion: APIVersion}
	cfg.applyDefaults()
	return cfg
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.App.ACMEChallenge) == "" {
		c.App.ACMEChallenge = ACMEChallengeHTTP01
	}
	if strings.TrimSpace(c.Nginx.ClientMaxBodySize) == "" {
		c.Nginx.ClientMaxBodySize = DefaultNginxClientMaxBodySize
	}
	if c.Nginx.HTTP2 == nil {
		enabled := true
		c.Nginx.HTTP2 = &enabled
	}
	if strings.TrimSpace(c.Nginx.Proxy.ReadTimeout) == "" {
		c.Nginx.Proxy.ReadTimeout = DefaultNginxProxyReadTimeout
	}
	if strings.TrimSpace(c.Nginx.Proxy.SendTimeout) == "" {
		c.Nginx.Proxy.SendTimeout = DefaultNginxProxySendTimeout
	}
}

func ExampleConfig() Config {
	cfg := New()
	cfg.App.Name = "example-app"
	cfg.App.Domains = []string{"abc.com", "www.abc.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/opt/example-app/example-app --listen 127.0.0.1:18001"
	cfg.Service.WorkingDirectory = "/opt/example-app"
	return cfg
}

func (c Config) Mode() Mode {
	switch {
	case strings.TrimSpace(c.App.Listen) != "":
		return ModeListen
	case strings.TrimSpace(c.App.Upstream) != "":
		return ModeUpstream
	default:
		return ""
	}
}

func (c Config) RequiresTailscale() bool {
	return c.Mode() == ModeUpstream || (c.Mode() == ModeListen && c.Tailscale.EnabledForListen)
}

func (c Config) PrimaryDomain() string {
	for _, domain := range c.App.Domains {
		if normalized := normalizeDomain(domain); normalized != "" {
			return normalized
		}
	}
	return ""
}

func (c Config) ResourceName() string {
	return strings.TrimSpace(c.App.Name)
}

func (c Config) NginxGoAccessCanonicalAccessLogPath() string {
	accessLog := strings.TrimSpace(c.Nginx.AccessLog)
	if accessLog != "" && accessLog != "off" {
		return accessLog
	}
	return filepath.Join("/var/log/meshify/apps", c.ResourceName(), "access.log")
}

func (c Config) NginxGoAccessManagesCanonicalAccessLog() bool {
	if !c.Nginx.GoAccess.Enabled {
		return false
	}
	accessLog := strings.TrimSpace(c.Nginx.AccessLog)
	if accessLog == "" {
		return true
	}
	cleanAccessLog := filepath.Clean(accessLog)
	return cleanAccessLog == filepath.Join("/var/log/meshify/apps", c.ResourceName(), "access.log")
}

func (c Config) NginxGoAccessDashboardPath() string {
	if value := strings.TrimSpace(c.Nginx.GoAccess.Path); value != "" {
		return value
	}
	return filepath.Join("/_meshify/apps", c.ResourceName(), DefaultNginxGoAccessPathSuffix)
}

func (c Config) NginxGoAccessWebSocketPath() string {
	if value := strings.TrimSpace(c.Nginx.GoAccess.WebSocketPath); value != "" {
		return value
	}
	return filepath.Join(c.NginxGoAccessDashboardPath(), DefaultNginxGoAccessWebSocketPathSuffix)
}

func (c Config) ServiceBinary() string {
	fields := strings.Fields(c.Service.ExecStart)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (c Config) EffectiveMeshifyConfig() string {
	if strings.TrimSpace(c.Tailscale.LoginServer) != "" {
		return ""
	}
	if path := strings.TrimSpace(c.Tailscale.MeshifyConfig); path != "" {
		return path
	}
	return DefaultMeshifyConfigPath
}

func (c Config) NginxHTTP2Enabled() bool {
	return c.Nginx.HTTP2Enabled()
}

func (n NginxConfig) HTTP2Enabled() bool {
	return n.HTTP2 == nil || *n.HTTP2
}

func (n NginxConfig) EffectiveClientMaxBodySize() string {
	if value := strings.TrimSpace(n.ClientMaxBodySize); value != "" {
		return value
	}
	return DefaultNginxClientMaxBodySize
}

func (g NginxGoAccessConfig) EffectiveLanguage() string {
	if value := strings.TrimSpace(g.Language); value != "" {
		return value
	}
	return DefaultNginxGoAccessLanguage
}

func (g NginxGoAccessConfig) EffectiveLogFormat() string {
	if value := strings.TrimSpace(g.LogFormat); value != "" {
		return value
	}
	return DefaultNginxGoAccessLogFormat
}

func (g NginxGoAccessConfig) EffectivePath() string {
	if value := strings.TrimSpace(g.Path); value != "" {
		return value
	}
	return filepath.Join("/_meshify/apps", "<app-name>", DefaultNginxGoAccessPathSuffix)
}

func (g NginxGoAccessConfig) EffectiveWebSocketPath() string {
	if value := strings.TrimSpace(g.WebSocketPath); value != "" {
		return value
	}
	return filepath.Join(g.EffectivePath(), DefaultNginxGoAccessWebSocketPathSuffix)
}

func EffectiveNginxGoAccessWebSocketListen(appName string, cfg NginxGoAccessConfig) string {
	if value := strings.TrimSpace(cfg.WebSocketListen); value != "" {
		return value
	}
	port := DefaultNginxGoAccessWebSocketPort(strings.TrimSpace(appName))
	return net.JoinHostPort(NginxGoAccessDefaultWebSocketHost, strconv.Itoa(port))
}

func DefaultNginxGoAccessWebSocketPort(appName string) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(appName))
	offset := int(hash.Sum32() % NginxGoAccessWebSocketPortSpan)
	for {
		port := NginxGoAccessWebSocketPortBase + offset
		if !isReservedMeshifyPort(port) {
			return port
		}
		offset = (offset + 1) % NginxGoAccessWebSocketPortSpan
	}
}

func (p NginxProxyConfig) EffectiveReadTimeout() string {
	if value := strings.TrimSpace(p.ReadTimeout); value != "" {
		return value
	}
	return DefaultNginxProxyReadTimeout
}

func (p NginxProxyConfig) EffectiveSendTimeout() string {
	if value := strings.TrimSpace(p.SendTimeout); value != "" {
		return value
	}
	return DefaultNginxProxySendTimeout
}
