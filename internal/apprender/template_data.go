package apprender

import (
	"meshify/internal/acme"
	"meshify/internal/appconfig"
	"meshify/internal/components/appsvc"
	"strconv"
	"strings"
)

type TemplateData struct {
	AppName           string
	VarPrefix         string
	Domains           []string
	ServerNames       string
	PrimaryDomain     string
	CertificateEmail  string
	ACMEChallenge     string
	DNSProvider       string
	DNSEnvFile        string
	UpstreamAddress   string
	SystemUser        string
	SystemGroup       string
	ExecStart         string
	WorkingDirectory  string
	ServiceEnvFile    string
	HTTP2             bool
	ClientMaxBodySize string
	AccessLog         string
	ErrorLog          string
	GoAccess          GoAccessTemplateData
	Proxy             ProxyTemplateData
	StaticLocations   []StaticLocationTemplateData
	WebrootPath       string
	LegoDataPath      string
	TLSDir            string
	FullchainPath     string
	PrivateKeyPath    string
	TLSMarkerPath     string
	HookPath          string
}

type GoAccessTemplateData struct {
	Enabled                    bool
	BinaryPath                 string
	Language                   string
	LogFormat                  string
	EnhancedLogFormat          bool
	CombinedLogFormat          bool
	NginxLogFormatName         string
	CanonicalAccessLogPath     string
	ManagedCanonicalAccessLog  bool
	DashboardPath              string
	WebSocketPath              string
	WebSocketHost              string
	WebSocketPort              int
	WebSocketListen            string
	WebSocketUpstream          string
	AuthBasicUserFile          string
	AuthCIDRAllowlist          []string
	ConfigPath                 string
	ReportDir                  string
	ReportPath                 string
	DBPath                     string
	ServiceUnit                string
	SystemUser                 string
	SystemGroup                string
	DashboardURL               string
	WSURL                      string
	Origin                     string
	HTMLReportTitle            string
	Lang                       string
	LCMessages                 string
	LCCType                    string
	LCTime                     string
	GoAccessLogFormatDirective string
}

type ProxyTemplateData struct {
	ConnectTimeout      string
	ReadTimeout         string
	SendTimeout         string
	BufferingSet        bool
	Buffering           string
	RequestBufferingSet bool
	RequestBuffering    string
}

type StaticLocationTemplateData struct {
	Exact        bool
	Path         string
	Alias        string
	DefaultType  string
	Expires      string
	CacheControl string
	TryFiles     bool
	GzipStatic   bool
	AccessLogOff bool
}

func NewTemplateData(cfg appconfig.Config) (TemplateData, error) {
	if err := cfg.Validate(); err != nil {
		return TemplateData{}, err
	}
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		return TemplateData{}, err
	}
	dnsProvider := strings.TrimSpace(cfg.DNS01.Provider)
	if cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01 && dnsProvider != "" {
		canonical, err := acme.CanonicalDNSProvider(dnsProvider)
		if err != nil {
			return TemplateData{}, err
		}
		dnsProvider = canonical
	}
	upstream := cfg.App.Listen
	if cfg.Mode() == appconfig.ModeUpstream {
		upstream = cfg.App.Upstream
	}
	return TemplateData{
		AppName:           names.AppName,
		VarPrefix:         names.VarPrefix,
		Domains:           append([]string(nil), cfg.App.Domains...),
		ServerNames:       strings.Join(cfg.App.Domains, " "),
		PrimaryDomain:     cfg.PrimaryDomain(),
		CertificateEmail:  cfg.App.CertificateEmail,
		ACMEChallenge:     cfg.App.ACMEChallenge,
		DNSProvider:       dnsProvider,
		DNSEnvFile:        cfg.DNS01.EnvFile,
		UpstreamAddress:   upstream,
		SystemUser:        names.SystemUser,
		SystemGroup:       names.SystemGroup,
		ExecStart:         cfg.Service.ExecStart,
		WorkingDirectory:  cfg.Service.WorkingDirectory,
		ServiceEnvFile:    cfg.Service.EnvFile,
		HTTP2:             cfg.Nginx.HTTP2Enabled(),
		ClientMaxBodySize: cfg.Nginx.EffectiveClientMaxBodySize(),
		AccessLog:         nginxAccessLogPath(cfg),
		ErrorLog:          cfg.Nginx.ErrorLog,
		GoAccess:          goAccessTemplateData(cfg, names),
		Proxy:             proxyTemplateData(cfg.Nginx.Proxy),
		StaticLocations:   staticLocationTemplateData(cfg.Nginx.StaticLocations),
		WebrootPath:       names.WebrootPath,
		LegoDataPath:      names.LegoDataPath,
		TLSDir:            names.TLSDir,
		FullchainPath:     names.FullchainPath,
		PrivateKeyPath:    names.PrivateKeyPath,
		TLSMarkerPath:     names.TLSMarkerPath,
		HookPath:          names.HookPath,
	}, nil
}

func nginxAccessLogPath(cfg appconfig.Config) string {
	if cfg.Nginx.GoAccess.Enabled {
		return appsvc.GoAccessCanonicalAccessLogPath(cfg)
	}
	return cfg.Nginx.AccessLog
}

func goAccessTemplateData(cfg appconfig.Config, names appsvc.Names) GoAccessTemplateData {
	goaccess := cfg.Nginx.GoAccess
	if !goaccess.Enabled {
		return GoAccessTemplateData{}
	}
	logFormat := goaccess.EffectiveLogFormat()
	webSocketUpstream := names.GoAccessWebSocketHost
	if strings.Contains(webSocketUpstream, ":") {
		webSocketUpstream = "[" + webSocketUpstream + "]"
	}
	webSocketUpstream += ":" + strconv.Itoa(names.GoAccessWebSocketPort)

	data := GoAccessTemplateData{
		Enabled:                   true,
		BinaryPath:                appsvc.GoAccessBinaryPath,
		Language:                  goaccess.EffectiveLanguage(),
		LogFormat:                 logFormat,
		EnhancedLogFormat:         logFormat == appconfig.NginxGoAccessLogFormatEnhanced,
		CombinedLogFormat:         logFormat == appconfig.NginxGoAccessLogFormatCombined,
		NginxLogFormatName:        names.GoAccessNginxLogFormatName,
		CanonicalAccessLogPath:    names.GoAccessCanonicalAccessLogPath,
		ManagedCanonicalAccessLog: appsvc.GoAccessManagesCanonicalAccessLog(cfg),
		DashboardPath:             cfg.NginxGoAccessDashboardPath(),
		WebSocketPath:             cfg.NginxGoAccessWebSocketPath(),
		WebSocketHost:             names.GoAccessWebSocketHost,
		WebSocketPort:             names.GoAccessWebSocketPort,
		WebSocketListen:           names.GoAccessWebSocketListen,
		WebSocketUpstream:         webSocketUpstream,
		AuthBasicUserFile:         goaccess.AuthBasicUserFile,
		AuthCIDRAllowlist:         append([]string(nil), goaccess.AuthCIDRAllowlist...),
		ConfigPath:                names.GoAccessConfigPath,
		ReportDir:                 names.GoAccessReportDir,
		ReportPath:                names.GoAccessReportPath,
		DBPath:                    names.GoAccessDBPath,
		ServiceUnit:               names.GoAccessServiceUnit,
		SystemUser:                names.GoAccessSystemUser,
		SystemGroup:               names.GoAccessSystemGroup,
		DashboardURL:              "https://" + cfg.PrimaryDomain() + cfg.NginxGoAccessDashboardPath(),
		WSURL:                     "wss://" + cfg.PrimaryDomain() + cfg.NginxGoAccessWebSocketPath(),
		Origin:                    "https://" + cfg.PrimaryDomain(),
		HTMLReportTitle:           "Meshify-GoAccess-" + names.AppName,
	}
	if data.EnhancedLogFormat {
		data.GoAccessLogFormatDirective = `%h %^ %^ [%x] "%r" %s %b "%R" "%u" "%v" %T "%^" "%^"`
	} else {
		data.GoAccessLogFormatDirective = "COMBINED"
	}
	if data.Language == appconfig.NginxGoAccessLanguageSimplifiedChinese {
		data.Lang = "zh_CN.UTF-8"
		data.LCMessages = "zh_CN.UTF-8"
		data.LCCType = "zh_CN.UTF-8"
		data.LCTime = "C.UTF-8"
	} else {
		data.Lang = "C.UTF-8"
		data.LCMessages = "C.UTF-8"
		data.LCCType = "C.UTF-8"
		data.LCTime = "C.UTF-8"
	}
	return data
}

func proxyTemplateData(proxy appconfig.NginxProxyConfig) ProxyTemplateData {
	data := ProxyTemplateData{
		ConnectTimeout: proxy.ConnectTimeout,
		ReadTimeout:    proxy.EffectiveReadTimeout(),
		SendTimeout:    proxy.EffectiveSendTimeout(),
	}
	if proxy.Buffering != nil {
		data.BufferingSet = true
		data.Buffering = nginxBool(*proxy.Buffering)
	}
	if proxy.RequestBuffering != nil {
		data.RequestBufferingSet = true
		data.RequestBuffering = nginxBool(*proxy.RequestBuffering)
	}
	return data
}

func staticLocationTemplateData(locations []appconfig.NginxStaticLocationConfig) []StaticLocationTemplateData {
	data := make([]StaticLocationTemplateData, 0, len(locations))
	for _, location := range locations {
		accessLogOff := false
		if location.AccessLog != nil {
			accessLogOff = !*location.AccessLog
		}
		data = append(data, StaticLocationTemplateData{
			Exact:        location.Match == "exact",
			Path:         location.Path,
			Alias:        location.Alias,
			DefaultType:  location.DefaultType,
			Expires:      location.Expires,
			CacheControl: location.CacheControl,
			TryFiles:     location.TryFiles,
			GzipStatic:   location.GzipStatic,
			AccessLogOff: accessLogOff,
		})
	}
	return data
}

func nginxBool(value bool) string {
	if value {
		return "on"
	}
	return "off"
}
