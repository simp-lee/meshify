package appsvc

import (
	"errors"
	"fmt"
	"meshify/internal/appconfig"
	"meshify/internal/host"
	"strings"
)

func EnableSiteCommand(names Names) host.Command {
	return host.Command{Name: "ln", Args: []string{"-sfn", names.NginxAvailablePath, names.NginxEnabledPath}}
}

func GuardEnabledSiteCommand(names Names) host.Command {
	script := `set -eu
enabled=$1
expected=$2
if [ ! -e "$enabled" ] && [ ! -L "$enabled" ]; then
    exit 0
fi
if [ -L "$enabled" ]; then
    target=$(readlink "$enabled")
    if [ "$target" = "$expected" ]; then
        exit 0
    fi
    printf '%s\n' "$enabled exists as a symlink to $target, not $expected; refusing to replace it" >&2
    exit 1
fi
printf '%s\n' "$enabled exists and is not a Meshify-managed app symlink; refusing to replace it" >&2
exit 1`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-app-nginx-enabled-guard", names.NginxEnabledPath, names.NginxAvailablePath},
		DisplayName: "guard-nginx-enabled-site",
		DisplayArgs: []string{names.NginxEnabledPath},
	}
}

func GuardServerNameConflictsCommand(names Names, domains []string) host.Command {
	return guardServerNameConflictsCommand(names.NginxEnabledPath, names.NginxAvailablePath, domains)
}

func guardServerNameConflictsCommand(currentEnabledPath string, currentAvailablePath string, domains []string) host.Command {
	script := `set -eu
current_enabled=$1
current_available=$2
domains=$3
dump=$(mktemp)
trap 'rm -f "$dump"' EXIT INT TERM

if ! nginx -T > "$dump" 2>&1; then
    cat "$dump" >&2
    exit 1
fi

awk -v current_enabled="$current_enabled" -v current_available="$current_available" -v domains="$domains" '
    function check_directive(text,    normalized, tail, count, parts, i) {
        normalized = text
        gsub(/[{};]/, " ", normalized)
        gsub(/[ \t\r\n]+/, " ", normalized)
        if (match(normalized, /(^| )server_name /)) {
            tail = substr(normalized, RSTART + RLENGTH)
            count = split(tail, parts, /[ \t]+/)
            for (i = 1; i <= count; i++) {
                if (parts[i] in wanted) {
                    printf "%s already appears in Nginx config %s; refusing duplicate app domain\n", parts[i], current_file > "/dev/stderr"
                    found = 1
                }
            }
        }
    }
    function process_line(text,    pos, part) {
        while (text != "") {
            pos = index(text, ";")
            if (pos == 0) {
                directive = directive " " text
                return
            }
            part = substr(text, 1, pos)
            directive = directive " " part
            check_directive(directive)
            directive = ""
            text = substr(text, pos + 1)
        }
    }
    BEGIN {
        split(domains, list, " ")
        for (i in list) {
            if (list[i] != "") wanted[list[i]] = 1
        }
        current_file = ""
        directive = ""
    }
    /^# configuration file / {
        directive = ""
        current_file = $0
        sub(/^# configuration file /, "", current_file)
        sub(/:$/, "", current_file)
        next
    }
    current_file == current_enabled || current_file == current_available {
        next
    }
    {
        line = $0
        sub(/[ \t]*#.*/, "", line)
        process_line(line)
    }
    END { exit found ? 1 : 0 }
' "$dump"`
	args := []string{"-c", script, "meshify-app-nginx-server-name-guard", strings.TrimSpace(currentEnabledPath), strings.TrimSpace(currentAvailablePath), strings.Join(domains, " ")}
	return host.Command{
		Name:        "sh",
		Args:        args,
		DisplayName: "guard-nginx-server-names",
		DisplayArgs: domains,
	}
}

func GuardDefaultServerCommand(names Names) host.Command {
	return guardDefaultServerCommand(names.NginxEnabledPath, names.NginxAvailablePath)
}

func guardDefaultServerCommand(currentEnabledPath string, currentAvailablePath string) host.Command {
	script := `set -eu
current_enabled=$1
current_available=$2
dump=$(mktemp)
trap 'rm -f "$dump"' EXIT INT TERM

if ! nginx -T > "$dump" 2>&1; then
    cat "$dump" >&2
    exit 1
fi

awk -v current_enabled="$current_enabled" -v current_available="$current_available" '
    function reset_section() {
        in_server = 0
        depth = 0
        block_text = ""
    }
    function check_section(    normalized, has_default, has_empty_name, has_444, has_421, has_meshify_tls) {
        if (current_file == "" || current_file == current_enabled || current_file == current_available) {
            return
        }
        normalized = block_text
        gsub(/[{};]/, " ", normalized)
        gsub(/[ \t\r\n]+/, " ", normalized)
        normalized = " " normalized " "
        has_default = index(normalized, " default_server ") > 0
        has_empty_name = normalized ~ / server_name "" /
        has_444 = normalized ~ / return 444 /
        has_421 = normalized ~ / return 421 /
        has_meshify_tls = index(block_text, "/etc/meshify/tls/") > 0
        if (has_default && !(has_empty_name && (has_444 || (has_421 && has_meshify_tls)))) {
            printf "%s declares default_server and is not a Meshify catch-all; remove or migrate it before deploying app sites\n", current_file > "/dev/stderr"
            failed = 1
        }
    }
    function count_char(text, char,    i, count) {
        count = 0
        for (i = 1; i <= length(text); i++) {
            if (substr(text, i, 1) == char) count++
        }
        return count
    }
    BEGIN {
        current_file = ""
        reset_section()
    }
    /^# configuration file / {
        if (in_server) check_section()
        current_file = $0
        sub(/^# configuration file /, "", current_file)
        sub(/:$/, "", current_file)
        reset_section()
        next
    }
    current_file == current_enabled || current_file == current_available {
        next
    }
    {
        line = $0
        sub(/[ \t]*#.*/, "", line)
        if (!in_server && line ~ /^[ \t]*server[ \t]*\{/) {
            reset_section()
            in_server = 1
        }
        if (in_server) {
            block_text = block_text "\n" line
            depth += count_char(line, "{") - count_char(line, "}")
            if (depth <= 0) {
                check_section()
                reset_section()
            }
        }
    }
    END {
        if (in_server) check_section()
        exit failed ? 1 : 0
    }
' "$dump"`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-app-nginx-default-server-guard", strings.TrimSpace(currentEnabledPath), strings.TrimSpace(currentAvailablePath)},
		DisplayName: "guard-nginx-default-server",
		DisplayArgs: []string{strings.TrimSpace(currentEnabledPath)},
	}
}

func TestNginxCommand() host.Command {
	return host.Command{Name: "nginx", Args: []string{"-t"}}
}

func ReloadNginxCommand() host.Command {
	return host.Command{Name: "systemctl", Args: []string{"reload-or-restart", "nginx.service"}}
}

func ValidateRenderedNginx(cfg appconfig.Config, names Names, content []byte) error {
	text := string(content)
	var errs []string
	httpBlock := nginxServerBlockContaining(text, "listen 80;")
	httpsBlock := nginxServerBlockContaining(text, "listen 443 ssl;")

	mustContain(&errs, text, "server_name "+strings.Join(cfg.App.Domains, " ")+";", "server_name domain list")
	mustContain(&errs, text, "server "+upstreamAddress(cfg)+";", "fixed upstream address")
	mustContain(&errs, text, "map $http_host $"+names.VarPrefix+"_host_header_valid", "Host allowlist map")
	mustContain(&errs, text, "map $http_host $"+names.VarPrefix+"_validated_host", "validated Host map")
	mustContain(&errs, text, "map $ssl_server_name $"+names.VarPrefix+"_sni_valid", "SNI allowlist map")
	mustContain(&errs, text, "root "+names.WebrootPath+";", "HTTP-01 webroot")
	mustContain(&errs, text, "ssl_certificate "+names.FullchainPath+";", "fullchain certificate path")
	mustContain(&errs, text, "ssl_certificate_key "+names.PrivateKeyPath+";", "private key path")
	if cfg.Nginx.HTTP2Enabled() {
		mustContain(&errs, text, "http2 on;", "HTTP/2 server directive")
	} else if strings.Contains(text, "http2 on;") {
		errs = append(errs, "HTTP/2 server directive must be absent when nginx.http2 is false")
	}
	mustContain(&errs, text, "client_max_body_size "+cfg.Nginx.EffectiveClientMaxBodySize()+";", "client_max_body_size")
	mustContain(&errs, text, "proxy_http_version 1.1;", "HTTP/1.1 reverse proxy")
	mustContain(&errs, text, "proxy_set_header Upgrade $http_upgrade;", "WebSocket Upgrade header")
	mustContain(&errs, text, "proxy_set_header Connection $"+names.VarPrefix+"_connection_upgrade;", "WebSocket Connection header")
	mustContain(&errs, text, "proxy_set_header Host $"+names.VarPrefix+"_validated_host;", "validated request Host forwarding")
	mustContain(&errs, text, "proxy_set_header X-Forwarded-Host $"+names.VarPrefix+"_validated_host;", "validated X-Forwarded-Host forwarding")
	mustContain(&errs, text, "location /.well-known/acme-challenge/", "ACME challenge location")
	if strings.Contains(text, "default_server") {
		errs = append(errs, "app Nginx site must not declare default_server")
	}
	hostMap := nginxMapBlock(text, "map $http_host $"+names.VarPrefix+"_host_header_valid")
	validatedHostMap := nginxMapBlock(text, "map $http_host $"+names.VarPrefix+"_validated_host")
	sniMap := nginxMapBlock(text, "map $ssl_server_name $"+names.VarPrefix+"_sni_valid")
	for _, domain := range cfg.App.Domains {
		mustContain(&errs, hostMap, `"`+domain+`" 1;`, "Host allowlist for "+domain)
		mustContain(&errs, hostMap, `"`+domain+`:80" 1;`, "HTTP Host allowlist with port for "+domain)
		mustContain(&errs, hostMap, `"`+domain+`:443" 1;`, "Host allowlist with port for "+domain)
		mustContain(&errs, validatedHostMap, `"`+domain+`" "`+domain+`";`, "validated Host map for "+domain)
		mustContain(&errs, validatedHostMap, `"`+domain+`:80" "`+domain+`";`, "validated Host map with HTTP port for "+domain)
		mustContain(&errs, validatedHostMap, `"`+domain+`:443" "`+domain+`";`, "validated Host map with HTTPS port for "+domain)
		mustContain(&errs, sniMap, `"`+domain+`" 1;`, "SNI allowlist for "+domain)
	}
	validateHTTPServerBlock(&errs, httpBlock, cfg, names)
	validateHTTPSServerBlock(&errs, httpsBlock, cfg, names)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validateHTTPServerBlock(errs *[]string, block string, cfg appconfig.Config, names Names) {
	if block == "" {
		*errs = append(*errs, "HTTP server block missing")
		return
	}
	mustContain(errs, block, "server_name "+strings.Join(cfg.App.Domains, " ")+";", "HTTP server_name domain list")
	mustContain(errs, block, "location /.well-known/acme-challenge/", "HTTP ACME challenge location")
	mustAppearBefore(errs, block,
		"if ($"+names.VarPrefix+"_host_header_valid = 0) {\n        return 421;\n    }",
		"location /.well-known/acme-challenge/",
		"HTTP Host allowlist before ACME challenge")
}

func validateHTTPSServerBlock(errs *[]string, block string, cfg appconfig.Config, names Names) {
	if block == "" {
		*errs = append(*errs, "HTTPS server block missing")
		return
	}
	mustContain(errs, block, "server_name "+strings.Join(cfg.App.Domains, " ")+";", "HTTPS server_name domain list")
	mustContain(errs, block, "ssl_certificate "+names.FullchainPath+";", "HTTPS fullchain certificate path")
	mustContain(errs, block, "ssl_certificate_key "+names.PrivateKeyPath+";", "HTTPS private key path")
	if cfg.Nginx.AccessLog != "" {
		mustContain(errs, block, "access_log "+cfg.Nginx.AccessLog+";", "HTTPS access_log")
	}
	if cfg.Nginx.ErrorLog != "" {
		mustContain(errs, block, "error_log "+cfg.Nginx.ErrorLog+";", "HTTPS error_log")
	}
	mustAppearBefore(errs, block,
		"if ($"+names.VarPrefix+"_sni_valid = 0) {\n        return 421;\n    }",
		"if ($"+names.VarPrefix+"_host_header_valid = 0) {\n        return 421;\n    }",
		"HTTPS SNI allowlist before Host allowlist")
	mustAppearBefore(errs, block,
		"if ($"+names.VarPrefix+"_host_header_valid = 0) {\n        return 421;\n    }",
		"location / {\n        proxy_pass",
		"HTTPS Host allowlist before proxy location")
	validateStaticLocations(errs, block, cfg.Nginx.StaticLocations)
	mustContain(errs, block, "proxy_set_header Host $"+names.VarPrefix+"_validated_host;", "HTTPS validated Host forwarding")
	mustContain(errs, block, "proxy_pass http://"+names.VarPrefix+"_upstream", "HTTPS proxy_pass app upstream")
	if cfg.Nginx.Proxy.ConnectTimeout != "" {
		mustContain(errs, block, "proxy_connect_timeout "+cfg.Nginx.Proxy.ConnectTimeout+";", "proxy connect timeout")
	}
	mustContain(errs, block, "proxy_read_timeout "+cfg.Nginx.Proxy.EffectiveReadTimeout()+";", "proxy read timeout")
	mustContain(errs, block, "proxy_send_timeout "+cfg.Nginx.Proxy.EffectiveSendTimeout()+";", "proxy send timeout")
	if cfg.Nginx.Proxy.Buffering != nil {
		mustContain(errs, block, "proxy_buffering "+nginxBool(*cfg.Nginx.Proxy.Buffering)+";", "proxy buffering")
	}
	if cfg.Nginx.Proxy.RequestBuffering != nil {
		mustContain(errs, block, "proxy_request_buffering "+nginxBool(*cfg.Nginx.Proxy.RequestBuffering)+";", "proxy request buffering")
	}
}

func validateStaticLocations(errs *[]string, httpsBlock string, locations []appconfig.NginxStaticLocationConfig) {
	for _, location := range locations {
		header := staticLocationHeader(location)
		block := nginxBlockStartingWith(httpsBlock, header)
		if block == "" {
			*errs = append(*errs, "static location "+location.Path+" missing")
			continue
		}
		mustAppearBefore(errs, httpsBlock, header, "location / {\n        proxy_pass", "static location "+location.Path+" before proxy location")
		mustContain(errs, block, "alias "+location.Alias+";", "static location "+location.Path+" alias")
		if location.Expires != "" {
			mustContain(errs, block, "expires "+location.Expires+";", "static location "+location.Path+" expires")
		}
		if location.CacheControl != "" {
			mustContain(errs, block, `add_header Cache-Control "`+location.CacheControl+`";`, "static location "+location.Path+" Cache-Control")
		}
		if location.TryFiles {
			mustContain(errs, block, "try_files $uri =404;", "static location "+location.Path+" try_files")
		}
		if location.GzipStatic {
			mustContain(errs, block, "gzip_static on;", "static location "+location.Path+" gzip_static")
		}
		if location.AccessLog != nil && !*location.AccessLog {
			mustContain(errs, block, "access_log off;", "static location "+location.Path+" access_log off")
		}
	}
}

func staticLocationHeader(location appconfig.NginxStaticLocationConfig) string {
	if location.Match == "exact" {
		return "location = " + location.Path + " {"
	}
	return "location " + location.Path + " {"
}

func nginxBool(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func upstreamAddress(cfg appconfig.Config) string {
	if cfg.Mode() == appconfig.ModeUpstream {
		return cfg.App.Upstream
	}
	return cfg.App.Listen
}

func mustContain(errs *[]string, text string, want string, label string) {
	if !strings.Contains(text, want) {
		*errs = append(*errs, fmt.Sprintf("%s missing", label))
	}
}

func mustAppearBefore(errs *[]string, text string, first string, second string, label string) {
	firstIndex := strings.Index(text, first)
	if firstIndex < 0 {
		*errs = append(*errs, label+" missing first clause")
		return
	}
	secondIndex := strings.Index(text, second)
	if secondIndex < 0 {
		*errs = append(*errs, label+" missing second clause")
		return
	}
	if firstIndex > secondIndex {
		*errs = append(*errs, label+" has wrong order")
	}
}

func nginxServerBlockContaining(text string, needle string) string {
	searchFrom := 0
	for {
		start := strings.Index(text[searchFrom:], "server {")
		if start < 0 {
			return ""
		}
		start += searchFrom
		block := nginxBlockAt(text, start)
		if block == "" {
			return ""
		}
		if strings.Contains(block, needle) {
			return block
		}
		searchFrom = start + len("server {")
	}
}

func nginxBlockAt(text string, start int) string {
	open := strings.Index(text[start:], "{")
	if open < 0 {
		return ""
	}
	open += start
	depth := 0
	for index := open; index < len(text); index++ {
		switch text[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : index+1]
			}
		}
	}
	return ""
}

func nginxMapBlock(text string, header string) string {
	start := strings.Index(text, header)
	if start < 0 {
		return ""
	}
	open := strings.Index(text[start:], "{")
	if open < 0 {
		return ""
	}
	open += start
	close := strings.Index(text[open:], "\n}")
	if close < 0 {
		return text[open:]
	}
	return text[start : open+close+2]
}

func nginxBlockStartingWith(text string, prefix string) string {
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	return nginxBlockAt(text, start)
}
