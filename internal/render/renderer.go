package render

import (
	"bytes"
	"fmt"
	"meshify/internal/acme"
	"meshify/internal/assets"
	"meshify/internal/config"
	"net/url"
	"strings"
	"text/template"
)

type TemplateData struct {
	ServerURL        string
	ServerName       string
	BaseDomain       string
	CertificateEmail string
	ACMEChallenge    string
	DNSProvider      string
	DNSEnvFile       string
	PublicIPv4       string
	PublicIPv6       string
}

type Renderer struct {
	loader assets.Loader
}

func NewRenderer(loader assets.Loader) Renderer {
	return Renderer{loader: loader}
}

func NewTemplateData(cfg config.Config) (TemplateData, error) {
	if err := cfg.Validate(); err != nil {
		return TemplateData{}, err
	}

	parsedURL, err := url.Parse(strings.TrimSpace(cfg.Default.ServerURL))
	if err != nil {
		return TemplateData{}, fmt.Errorf("parse default.server_url: %w", err)
	}

	dnsProvider := strings.TrimSpace(cfg.Advanced.DNS01.Provider)
	if strings.TrimSpace(cfg.Default.ACMEChallenge) == config.ACMEChallengeDNS01 && dnsProvider != "" {
		canonical, err := acme.CanonicalDNSProvider(dnsProvider)
		if err != nil {
			return TemplateData{}, err
		}
		dnsProvider = canonical
	}

	return TemplateData{
		ServerURL:        strings.TrimSpace(cfg.Default.ServerURL),
		ServerName:       parsedURL.Hostname(),
		BaseDomain:       strings.TrimSpace(cfg.Default.BaseDomain),
		CertificateEmail: strings.TrimSpace(cfg.Default.CertificateEmail),
		ACMEChallenge:    strings.TrimSpace(cfg.Default.ACMEChallenge),
		DNSProvider:      dnsProvider,
		DNSEnvFile:       strings.TrimSpace(cfg.Advanced.DNS01.EnvFile),
		PublicIPv4:       strings.TrimSpace(cfg.Advanced.Network.PublicIPv4),
		PublicIPv6:       strings.TrimSpace(cfg.Advanced.Network.PublicIPv6),
	}, nil
}

func (renderer Renderer) Render(asset assets.Asset, data TemplateData) ([]byte, error) {
	source, err := renderer.loader.Read(asset.SourcePath)
	if err != nil {
		return nil, err
	}

	if asset.ContentMode == assets.ContentModeCopy {
		return source, nil
	}

	tmpl, err := template.New(asset.SourcePath).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("parse template %q: %w", asset.SourcePath, err)
	}

	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render template %q: %w", asset.SourcePath, err)
	}

	return output.Bytes(), nil
}
