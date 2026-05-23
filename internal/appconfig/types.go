package appconfig

const (
	APIVersion               = "meshify/app/v1alpha1"
	DefaultConfigPath        = "meshify-app.yaml"
	DefaultMeshifyConfigPath = "meshify.yaml"

	DefaultNginxClientMaxBodySize = "20m"
	DefaultNginxProxyReadTimeout  = "600s"
	DefaultNginxProxySendTimeout  = "600s"

	ACMEChallengeHTTP01 = "http-01"
	ACMEChallengeDNS01  = "dns-01"

	ModeListen   Mode = "listen"
	ModeUpstream Mode = "upstream"
)

type Mode string

type Config struct {
	APIVersion string          `yaml:"api_version"`
	App        AppConfig       `yaml:"app"`
	Service    ServiceConfig   `yaml:"service"`
	Nginx      NginxConfig     `yaml:"nginx"`
	DNS01      DNS01Config     `yaml:"dns01"`
	Tailscale  TailscaleConfig `yaml:"tailscale"`
}

type AppConfig struct {
	Name             string   `yaml:"name"`
	Domains          []string `yaml:"domains"`
	CertificateEmail string   `yaml:"certificate_email"`
	ACMEChallenge    string   `yaml:"acme_challenge"`
	Listen           string   `yaml:"listen"`
	Upstream         string   `yaml:"upstream"`
}

type ServiceConfig struct {
	ExecStart        string `yaml:"exec_start"`
	WorkingDirectory string `yaml:"working_directory"`
	EnvFile          string `yaml:"env_file"`
}

type NginxConfig struct {
	ClientMaxBodySize string                      `yaml:"client_max_body_size"`
	HTTP2             *bool                       `yaml:"http2"`
	AccessLog         string                      `yaml:"access_log"`
	ErrorLog          string                      `yaml:"error_log"`
	Proxy             NginxProxyConfig            `yaml:"proxy"`
	StaticLocations   []NginxStaticLocationConfig `yaml:"static_locations"`
}

type NginxProxyConfig struct {
	ConnectTimeout   string `yaml:"connect_timeout"`
	ReadTimeout      string `yaml:"read_timeout"`
	SendTimeout      string `yaml:"send_timeout"`
	Buffering        *bool  `yaml:"buffering"`
	RequestBuffering *bool  `yaml:"request_buffering"`
}

type NginxStaticLocationConfig struct {
	Path         string `yaml:"path"`
	Match        string `yaml:"match"`
	Alias        string `yaml:"alias"`
	DefaultType  string `yaml:"default_type"`
	Expires      string `yaml:"expires"`
	CacheControl string `yaml:"cache_control"`
	TryFiles     bool   `yaml:"try_files"`
	GzipStatic   bool   `yaml:"gzip_static"`
	AccessLog    *bool  `yaml:"access_log"`
}

type DNS01Config struct {
	Provider string `yaml:"provider"`
	EnvFile  string `yaml:"env_file"`
}

type TailscaleConfig struct {
	EnabledForListen bool   `yaml:"enabled_for_listen"`
	MeshifyConfig    string `yaml:"meshify_config"`
	LoginServer      string `yaml:"login_server"`
	Hostname         string `yaml:"hostname"`
	AuthKeyFile      string `yaml:"auth_key_file"`
}
