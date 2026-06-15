package appsvc

import (
	"fmt"
	"lanpanel/internal/acme"
	"lanpanel/internal/appconfig"
	"lanpanel/internal/host"
	"strings"
)

type CertificatePlan struct {
	Command host.Command
}

func NewCertificatePlan(cfg appconfig.Config, names Names) (CertificatePlan, error) {
	if err := cfg.Validate(); err != nil {
		return CertificatePlan{}, err
	}
	args := []string{
		"run",
		"--path", names.LegoDataPath,
		"--email", cfg.App.CertificateEmail,
	}
	for _, domain := range cfg.App.Domains {
		args = append(args, "--domains", domain)
	}
	args = append(args, "--accept-tos")
	switch cfg.App.ACMEChallenge {
	case appconfig.ACMEChallengeHTTP01:
		args = append(args, "--http", "--http.webroot", names.WebrootPath)
	case appconfig.ACMEChallengeDNS01:
		provider, err := acme.CanonicalDNSProvider(cfg.DNS01.Provider)
		if err != nil {
			return CertificatePlan{}, err
		}
		args = append(args, "--dns", provider)
	default:
		return CertificatePlan{}, fmt.Errorf("unsupported ACME challenge %q", cfg.App.ACMEChallenge)
	}
	args = append(args, "--force-cert-domains")
	command := legoIssueOrRenewCommand(names, cfg.PrimaryDomain(), args)
	if cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01 && strings.TrimSpace(cfg.DNS01.EnvFile) != "" {
		command = commandWithEnvFile(cfg.DNS01.EnvFile, command)
	}
	return CertificatePlan{Command: command}, nil
}

func legoIssueOrRenewCommand(names Names, primaryDomain string, legoArgs []string) host.Command {
	script := `set -eu
lego=$1
lego_path=$2
primary_domain=$3
hook=$4
shift 4
cert="$lego_path/certificates/$primary_domain.crt"
key="$lego_path/certificates/$primary_domain.key"
metadata="$lego_path/certificates/$primary_domain.json"
if [ -s "$cert" ] && [ -s "$key" ] && [ -s "$metadata" ]; then
    LEGO_HOOK_CERT_PATH="$cert" LEGO_HOOK_CERT_KEY_PATH="$key" "$hook"
fi
exec "$lego" "$@" --deploy-hook "$hook"`
	args := []string{"-c", script, "lanpanel-app-lego-issue-or-renew", LegoBinaryPath, names.LegoDataPath, strings.TrimSpace(primaryDomain), names.HookPath}
	args = append(args, legoArgs...)
	displayArgs := append([]string(nil), legoArgs...)
	displayArgs = append(displayArgs, "--deploy-hook", names.HookPath)
	return host.Command{
		Name:        "sh",
		Args:        args,
		DisplayName: LegoBinaryPath,
		DisplayArgs: displayArgs,
	}
}

func GuardTLSOwnershipCommand(names Names) host.Command {
	script := `set -eu
app_name=$1
tls_dir=$2
marker=$3
fullchain=$4
privkey=$5
expected_marker="Lanpanel-managed: app.name=$app_name"

if [ -e "$marker" ]; then
    actual_marker=$(cat "$marker")
    if [ "$actual_marker" = "$expected_marker" ]; then
        exit 0
    fi
    echo "$marker is managed by a different Lanpanel app; refusing to write app TLS files" >&2
    exit 1
fi

for target in "$fullchain" "$privkey"; do
    if [ -e "$target" ]; then
        echo "$target exists but $marker is missing; refusing to overwrite non-Lanpanel TLS file" >&2
        exit 1
    fi
done

install -d -m 0755 "$tls_dir"
tmp=$(mktemp "$tls_dir/.lanpanel-managed.XXXXXX")
trap 'rm -f "$tmp"' EXIT INT TERM
printf '%s\n' "$expected_marker" > "$tmp"
chmod 0600 "$tmp"
mv "$tmp" "$marker"`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "lanpanel-app-tls-ownership-guard", names.AppName, names.TLSDir, names.TLSMarkerPath, names.FullchainPath, names.PrivateKeyPath},
		DisplayName: "guard-app-tls",
		DisplayArgs: []string{names.TLSDir},
	}
}

func HTTP01BootstrapCommands(names Names) []host.Command {
	script := `set -eu
fullchain=$1
privkey=$2
server_name=$3
if [ ! -s "$fullchain" ] || [ ! -s "$privkey" ]; then
    openssl req -x509 -nodes -newkey rsa:2048 -days 1 -subj "/CN=$server_name" -keyout "$privkey" -out "$fullchain"
    chmod 0600 "$privkey"
    chmod 0644 "$fullchain"
fi`
	return []host.Command{
		{Name: "mkdir", Args: []string{"-p", "-m", "0755", "--", names.WebrootPath, names.LegoDataPath, names.TLSDir}},
		{Name: "sh", Args: []string{"-c", script, "lanpanel-app-tls-bootstrap", names.FullchainPath, names.PrivateKeyPath, names.AppName}},
	}
}

func commandWithEnvFile(envFile string, command host.Command) host.Command {
	script := `set -eu
env_file=$1
shift
trim_lanpanel_env_value() {
    value=$1
    while :; do
        case "$value" in
            " "*) value=${value# } ;;
            "	"*) value=${value#	} ;;
            *) break ;;
        esac
    done
    while :; do
        case "$value" in
            *" ") value=${value% } ;;
            *"	") value=${value%	} ;;
            *) break ;;
        esac
    done
    printf '%s' "$value"
}
while IFS= read -r line || [ -n "$line" ]; do
    line=$(trim_lanpanel_env_value "$line")
    case "$line" in
        ""|"#"*|";"*) continue ;;
        export\ *)
            echo "unsupported export syntax in DNS env_file" >&2
            exit 64
            ;;
        *=*) ;;
        *) continue ;;
    esac
    key=$(trim_lanpanel_env_value "${line%%=*}")
    value=$(trim_lanpanel_env_value "${line#*=}")
    case "$key" in
        ""|[0-9]*|*[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_]*)
            echo "unsupported DNS env_file variable name" >&2
            exit 64
            ;;
    esac
    case "$value" in
        \"*\") value=${value#\"}; value=${value%\"} ;;
        \'*\') value=${value#\'}; value=${value%\'} ;;
    esac
    [ -n "$value" ] || continue
    export "$key=$value"
done < "$env_file"
exec "$@"`
	args := []string{"-c", script, "lanpanel-app-lego-dns01", strings.TrimSpace(envFile), command.Name}
	args = append(args, command.Args...)
	displayName := command.DisplayName
	if strings.TrimSpace(displayName) == "" {
		displayName = command.Name
	}
	displayArgs := command.DisplayArgs
	if displayArgs == nil {
		displayArgs = command.Args
	}
	return host.Command{
		Name:        "sh",
		Args:        args,
		DisplayName: displayName,
		DisplayArgs: append([]string(nil), displayArgs...),
	}
}

func legoCommandWithEnvFile(envFile string, legoArgs []string) host.Command {
	return commandWithEnvFile(envFile, host.Command{
		Name:        LegoBinaryPath,
		Args:        append([]string(nil), legoArgs...),
		DisplayName: LegoBinaryPath,
		DisplayArgs: append([]string(nil), legoArgs...),
	})
}
