package nginx

import (
	"errors"
	"fmt"
	tlscomponent "meshify/internal/components/tls"
	"meshify/internal/config"
	"net/url"
	"strings"
)

const (
	SiteAvailablePath        = "/etc/nginx/sites-available/headscale.conf"
	SiteEnabledPath          = "/etc/nginx/sites-enabled/headscale.conf"
	DefaultSiteAvailablePath = "/etc/nginx/sites-available/default"
	DefaultSiteEnabledPath   = "/etc/nginx/sites-enabled/default"
	ACMEWebroot              = tlscomponent.WebrootPath
	UpstreamAddress          = "127.0.0.1:8080"
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
		Fullchain:  tlscomponent.StableFullchainPath(serverName),
		PrivKey:    tlscomponent.StablePrivateKeyPath(serverName),
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
	if strings.Contains(text, "listen 443 ssl http2") || strings.Contains(text, "listen [::]:443 ssl http2") {
		errs = append(errs, "meshify Nginx site must not use deprecated listen ... http2 syntax")
	}
	validateServerNameIsolation(&errs, site, text)
	validateHostSNIGuards(&errs, site, text)
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

func validateServerNameIsolation(errs *[]string, site SiteConfig, text string) {
	blocks := nginxServerBlocks(text)
	httpDefaultIPv4 := false
	httpDefaultIPv6 := false
	httpsDefaultIPv4 := false
	httpsDefaultIPv6 := false
	for _, block := range blocks {
		if strings.Contains(block, "server_name _;") {
			*errs = append(*errs, "meshify Nginx site must not define pseudo catch-all server_name _")
		}
		if !strings.Contains(block, "default_server") {
			continue
		}
		if strings.Contains(block, "server_name "+site.ServerName+";") {
			*errs = append(*errs, "meshify named Nginx site must not be the default_server")
		}
		if strings.Contains(block, "proxy_pass ") || strings.Contains(block, "headscale_upstream") {
			*errs = append(*errs, "meshify default_server catch-all must not proxy to Headscale")
		}
		if !strings.Contains(block, `server_name "";`) {
			*errs = append(*errs, `meshify default_server catch-all must use empty server_name ""`)
		}
		hasHTTPDefault := strings.Contains(block, "listen 80 default_server") || strings.Contains(block, "listen [::]:80 default_server")
		hasHTTPSDefault := strings.Contains(block, "listen 443 ssl default_server") || strings.Contains(block, "listen [::]:443 ssl default_server")
		switch {
		case hasHTTPDefault:
			httpDefaultIPv4 = httpDefaultIPv4 || strings.Contains(block, "listen 80 default_server")
			httpDefaultIPv6 = httpDefaultIPv6 || strings.Contains(block, "listen [::]:80 default_server")
			if !strings.Contains(block, "return 444;") {
				*errs = append(*errs, "meshify HTTP default_server catch-all must close unmatched requests with 444")
			}
		case hasHTTPSDefault:
			httpsDefaultIPv4 = httpsDefaultIPv4 || strings.Contains(block, "listen 443 ssl default_server")
			httpsDefaultIPv6 = httpsDefaultIPv6 || strings.Contains(block, "listen [::]:443 ssl default_server")
			if !strings.Contains(block, "ssl_certificate "+site.Fullchain+";") || !strings.Contains(block, "ssl_certificate_key "+site.PrivKey+";") {
				*errs = append(*errs, "meshify HTTPS default_server catch-all must use stable meshify certificate paths")
			}
			if !strings.Contains(block, "return 421;") {
				*errs = append(*errs, "meshify HTTPS default_server catch-all must reject unmatched SNI/Host with 421")
			}
		}
	}
	if !httpDefaultIPv4 || !httpDefaultIPv6 {
		*errs = append(*errs, "meshify Nginx site missing HTTP default_server catch-all")
	}
	if !httpsDefaultIPv4 || !httpsDefaultIPv6 {
		*errs = append(*errs, "meshify Nginx site missing HTTPS default_server catch-all")
	}
}

func validateHostSNIGuards(errs *[]string, site SiteConfig, text string) {
	hostMap := nginxBlockStartingWith(text, "map $http_host $meshify_host_header_valid")
	if hostMap == "" {
		*errs = append(*errs, "HTTPS Host guard map missing")
	} else {
		for _, want := range []struct {
			text  string
			label string
		}{
			{text: "default 0;", label: "HTTPS Host guard default deny"},
			{text: `"` + site.ServerName + `" 1;`, label: "HTTPS Host guard bare host allowlist"},
			{text: `"` + site.ServerName + `:443" 1;`, label: "HTTPS Host guard port host allowlist"},
		} {
			mustContain(errs, hostMap, want.text, want.label)
		}
	}
	sniMap := nginxBlockStartingWith(text, "map $ssl_server_name $meshify_sni_valid")
	if sniMap == "" {
		*errs = append(*errs, "HTTPS SNI guard map missing")
	} else {
		for _, want := range []struct {
			text  string
			label string
		}{
			{text: "default 0;", label: "HTTPS SNI guard default deny"},
			{text: `"` + site.ServerName + `" 1;`, label: "HTTPS SNI guard allowlist"},
		} {
			mustContain(errs, sniMap, want.text, want.label)
		}
	}

	block := namedHTTPSServerBlock(site, text)
	if block == "" {
		*errs = append(*errs, "meshify named HTTPS server block missing")
		return
	}
	proxyIndex := strings.Index(block, "proxy_pass ")
	for _, guard := range []string{
		"if ($meshify_sni_valid = 0)",
		"if ($meshify_host_header_valid = 0)",
	} {
		guardIndex := strings.Index(block, guard)
		if guardIndex < 0 {
			*errs = append(*errs, "meshify named HTTPS server block missing "+guard+" guard")
			continue
		}
		returnIndex := strings.Index(block[guardIndex:], "return 421;")
		if returnIndex < 0 {
			*errs = append(*errs, "meshify named HTTPS server block "+guard+" guard must return 421")
		}
		if proxyIndex >= 0 && guardIndex > proxyIndex {
			*errs = append(*errs, "meshify named HTTPS server block must reject Host/SNI mismatches before proxying")
		}
	}
}

func namedHTTPSServerBlock(site SiteConfig, text string) string {
	for _, block := range nginxServerBlocks(text) {
		if strings.Contains(block, "server_name "+site.ServerName+";") &&
			strings.Contains(block, "listen 443 ssl;") &&
			!strings.Contains(block, "default_server") {
			return block
		}
	}
	return ""
}

func nginxBlockStartingWith(text string, prefix string) string {
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	open := strings.Index(text[start:], "{")
	if open < 0 {
		return ""
	}
	start += open
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
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
