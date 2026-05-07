package tls

import (
	"fmt"
	"net/url"
	"strings"

	"meshify/internal/config"
	"meshify/internal/host"
)

type CertificatePlan struct {
	ServerName string
	Email      string
	Challenge  ChallengePlan
	Fullchain  string
	PrivKey    string
	Command    host.Command
}

func NewCertificatePlan(cfg config.Config) (CertificatePlan, error) {
	challenge, err := NewChallengePlan(cfg)
	if err != nil {
		return CertificatePlan{}, err
	}
	parsedURL, err := url.Parse(strings.TrimSpace(cfg.Default.ServerURL))
	if err != nil {
		return CertificatePlan{}, err
	}
	serverName := strings.TrimSpace(parsedURL.Hostname())
	if serverName == "" {
		return CertificatePlan{}, fmt.Errorf("server URL host is required")
	}
	email := strings.TrimSpace(cfg.Default.CertificateEmail)
	args := []string{
		"--path", LegoDataPath,
		"--email", email,
		"--domains", serverName,
		"--accept-tos",
	}
	switch challenge.Challenge {
	case config.ACMEChallengeHTTP01:
		args = append(args, "--http", "--http.webroot", challenge.Webroot)
	case config.ACMEChallengeDNS01:
		args = append(args, "--dns", challenge.Provider)
	default:
		return CertificatePlan{}, fmt.Errorf("unsupported ACME challenge %q", challenge.Challenge)
	}
	args = append(args, "run", "--run-hook", RunHookPath)

	command := host.Command{Name: LegoBinaryPath, Args: args}
	if challenge.Challenge == config.ACMEChallengeDNS01 && strings.TrimSpace(challenge.EnvFile) != "" {
		command = legoCommandWithEnvFile(challenge.EnvFile, args)
	}
	return CertificatePlan{
		ServerName: serverName,
		Email:      email,
		Challenge:  challenge,
		Fullchain:  StableFullchainPath(serverName),
		PrivKey:    StablePrivateKeyPath(serverName),
		Command:    command,
	}, nil
}

func HTTP01BootstrapCommands(serverName string) []host.Command {
	serverName = strings.TrimSpace(serverName)
	fullchain := StableFullchainPath(serverName)
	privKey := StablePrivateKeyPath(serverName)
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
		{Name: "mkdir", Args: []string{"-p", "-m", "0755", "--", WebrootPath, LegoDataPath, StableTLSDir(serverName)}},
		{Name: "sh", Args: []string{"-c", script, "meshify-tls-bootstrap", fullchain, privKey, serverName}},
	}
}

func legoCommandWithEnvFile(envFile string, legoArgs []string) host.Command {
	script := `set -eu
env_file=$1
shift
trim_meshify_env_value() {
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
    line=$(trim_meshify_env_value "$line")
    case "$line" in
        ""|"#"*|";"*) continue ;;
        export\ *)
            echo "unsupported export syntax in DNS env_file" >&2
            exit 64
            ;;
        *=*) ;;
        *) continue ;;
    esac
    key=$(trim_meshify_env_value "${line%%=*}")
    value=$(trim_meshify_env_value "${line#*=}")
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
	args := []string{"-c", script, "meshify-lego-dns01", strings.TrimSpace(envFile), LegoBinaryPath}
	args = append(args, legoArgs...)
	return host.Command{
		Name:        "sh",
		Args:        args,
		DisplayName: LegoBinaryPath,
		DisplayArgs: append([]string(nil), legoArgs...),
	}
}
