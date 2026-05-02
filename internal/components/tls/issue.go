package tls

import (
	"fmt"
	"meshify/internal/config"
	"meshify/internal/host"
	"net/url"
	"strings"
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
		"certonly",
		"--non-interactive",
		"--agree-tos",
		"--email", email,
		"--cert-name", serverName,
		"-d", serverName,
		"--deploy-hook", DeployHookPath,
	}
	switch challenge.Challenge {
	case config.ACMEChallengeHTTP01:
		args = append(args, "--webroot", "--webroot-path", challenge.Webroot)
	case config.ACMEChallengeDNS01:
		pluginName, err := DNSPluginName(challenge.Provider)
		if err != nil {
			return CertificatePlan{}, err
		}
		args = append(args, "--authenticator", pluginName, "--preferred-challenges", "dns")
	default:
		return CertificatePlan{}, fmt.Errorf("unsupported ACME challenge %q", challenge.Challenge)
	}
	return CertificatePlan{
		ServerName: serverName,
		Email:      email,
		Challenge:  challenge,
		Fullchain:  "/etc/letsencrypt/live/" + serverName + "/fullchain.pem",
		PrivKey:    "/etc/letsencrypt/live/" + serverName + "/privkey.pem",
		Command:    host.Command{Name: "certbot", Args: args},
	}, nil
}

func HTTP01BootstrapCommands(serverName string) []host.Command {
	serverName = strings.TrimSpace(serverName)
	fullchain := "/etc/letsencrypt/live/" + serverName + "/fullchain.pem"
	privKey := "/etc/letsencrypt/live/" + serverName + "/privkey.pem"
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
		{Name: "mkdir", Args: []string{"-p", "-m", "0755", "--", WebrootPath}},
		{Name: "mkdir", Args: []string{"-p", "-m", "0755", "--", "/etc/letsencrypt/live/" + serverName}},
		{Name: "sh", Args: []string{"-c", script, "meshify-tls-bootstrap", fullchain, privKey, serverName}},
	}
}

func RenewDryRunCommand(runDeployHooks bool) host.Command {
	args := []string{"renew", "--dry-run", "--no-random-sleep-on-renew"}
	if runDeployHooks {
		args = append(args, "--run-deploy-hooks")
	}
	return host.Command{Name: "certbot", Args: args}
}
