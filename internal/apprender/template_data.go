package apprender

import (
	"meshify/internal/acme"
	"meshify/internal/appconfig"
	"meshify/internal/components/appsvc"
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
		AccessLog:         cfg.Nginx.AccessLog,
		ErrorLog:          cfg.Nginx.ErrorLog,
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
