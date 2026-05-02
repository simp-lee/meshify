package nginx

import (
	"errors"
	"fmt"
	"meshify/internal/config"
	"net/url"
	"strings"
)

const (
	SiteAvailablePath = "/etc/nginx/sites-available/headscale.conf"
	SiteEnabledPath   = "/etc/nginx/sites-enabled/headscale.conf"
	ACMEWebroot       = "/var/www/certbot"
	UpstreamAddress   = "127.0.0.1:8080"
)

type SiteConfig struct {
	ServerName string
	Upstream   string
	Webroot    string
	Fullchain  string
	PrivKey    string
}

func NewSiteConfig(cfg config.Config) (SiteConfig, error) {
	if err := cfg.Validate(); err != nil {
		return SiteConfig{}, err
	}
	parsedURL, err := url.Parse(strings.TrimSpace(cfg.Default.ServerURL))
	if err != nil {
		return SiteConfig{}, err
	}
	serverName := strings.TrimSpace(parsedURL.Hostname())
	if serverName == "" {
		return SiteConfig{}, fmt.Errorf("server URL host is required")
	}
	return SiteConfig{
		ServerName: serverName,
		Upstream:   UpstreamAddress,
		Webroot:    ACMEWebroot,
		Fullchain:  "/etc/letsencrypt/live/" + serverName + "/fullchain.pem",
		PrivKey:    "/etc/letsencrypt/live/" + serverName + "/privkey.pem",
	}, nil
}

func ValidateRenderedSite(site SiteConfig, content []byte) error {
	text := string(content)
	var errs []string
	mustContain(&errs, text, "server_name "+site.ServerName+";", "server_name isolation")
	mustContain(&errs, text, "server "+site.Upstream+";", "Headscale upstream must stay loopback")
	mustContain(&errs, text, "root "+site.Webroot+";", "HTTP-01 webroot")
	mustContain(&errs, text, "ssl_certificate "+site.Fullchain+";", "fullchain certificate")
	mustContain(&errs, text, "ssl_certificate_key "+site.PrivKey+";", "private key path")
	mustContain(&errs, text, "proxy_http_version 1.1;", "HTTP/1.1 reverse proxy")
	mustContain(&errs, text, "proxy_set_header Upgrade $http_upgrade;", "WebSocket Upgrade header")
	mustContain(&errs, text, "proxy_set_header Connection $connection_upgrade;", "WebSocket Connection header")
	mustContain(&errs, text, "location /.well-known/acme-challenge/", "ACME challenge location")
	validateDefaultServerIsolation(&errs, site, text)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func mustContain(errs *[]string, text string, want string, label string) {
	if !strings.Contains(text, want) {
		*errs = append(*errs, label+" missing")
	}
}

func validateDefaultServerIsolation(errs *[]string, site SiteConfig, text string) {
	blocks := nginxServerBlocks(text)
	hasHTTPDefault := false
	hasHTTPSDefault := false
	for _, block := range blocks {
		if strings.Contains(block, "server_name "+site.ServerName+";") && strings.Contains(block, "default_server") {
			*errs = append(*errs, "Headscale server block must not claim default_server")
		}
		if !strings.Contains(block, "server_name _;") {
			continue
		}
		if strings.Contains(block, "proxy_pass http://headscale_upstream") || strings.Contains(block, "server "+site.Upstream+";") {
			*errs = append(*errs, "catch-all default server must not proxy to Headscale")
		}
		if strings.Contains(block, "listen 80 default_server;") && strings.Contains(block, "return 444;") {
			hasHTTPDefault = true
		}
		if strings.Contains(block, "listen 443 ssl http2 default_server;") && strings.Contains(block, "return 421;") {
			hasHTTPSDefault = true
		}
	}
	if !hasHTTPDefault {
		*errs = append(*errs, "HTTP catch-all default server missing")
	}
	if !hasHTTPSDefault {
		*errs = append(*errs, "HTTPS catch-all default server missing")
	}
}

func nginxServerBlocks(text string) []string {
	blocks := []string{}
	for searchFrom := 0; searchFrom < len(text); {
		start := strings.Index(text[searchFrom:], "server {")
		if start < 0 {
			break
		}
		start += searchFrom
		depth := 0
		for i := start; i < len(text); i++ {
			switch text[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					blocks = append(blocks, text[start:i+1])
					searchFrom = i + 1
					goto nextBlock
				}
			}
		}
		break
	nextBlock:
	}
	return blocks
}
