package apppreflight

import (
	"fmt"
	"meshify/internal/appconfig"
	"meshify/internal/preflight"
	"net/netip"
	"strings"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusWarn Status = "warn"
)

type Check struct {
	ID           string
	Status       Status
	Summary      string
	Remediations []string
}

type Inputs struct {
	Permissions                 preflight.PermissionState
	DNS                         map[string]preflight.DNSProbe
	Ports                       []preflight.PortBinding
	ServiceBinaryOK             bool
	ServiceBinaryPath           string
	ServiceEnvFile              string
	ServiceEnvFileChecked       bool
	ServiceEnvFileReady         bool
	ServiceEnvFileDetail        string
	TailscaleRequired           bool
	TailscaleAuthKeyFile        string
	TailscaleAuthKeyFileChecked bool
	TailscaleAuthKeyFileReady   bool
	TailscaleAuthKeyFileDetail  string
	DNSCredentialsChecked       bool
	DNSCredentialsReady         bool
	DNSCredentialsDetail        string
}

type Report struct {
	Checks []Check
}

func BuildReport(cfg appconfig.Config, inputs Inputs) Report {
	checks := []Check{}
	add := func(id string, status Status, summary string, remediations ...string) {
		checks = append(checks, Check{ID: id, Status: status, Summary: summary, Remediations: compact(remediations)})
	}

	if inputs.Permissions.IsRoot {
		add("permissions", StatusPass, "当前命令具备 root 权限")
	} else {
		add("permissions", StatusFail, "app deploy 需要当前进程具备 root 权限", "使用 sudo meshify app deploy 重新执行。")
	}

	for _, domain := range cfg.App.Domains {
		probe, ok := inputs.DNS[domain]
		status, summary, remediation := evaluateDNSProbe(cfg, domain, probe, ok)
		add("dns:"+domain, status, summary, remediation)
	}

	addPortChecks(&checks, inputs.Ports)

	if cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01 {
		switch {
		case !inputs.DNSCredentialsChecked:
			add("dns01-credentials", StatusFail, "DNS-01 provider env_file 未完成自动校验", "准备 root-only 的 dns01.env_file，或确认该 provider 支持当前主机身份的环境凭据。")
		case !inputs.DNSCredentialsReady:
			detail := strings.TrimSpace(inputs.DNSCredentialsDetail)
			if detail == "" {
				detail = "DNS-01 provider env_file 不可用"
			} else {
				detail = "DNS-01 provider env_file 校验失败: " + detail
			}
			add("dns01-credentials", StatusFail, detail, "修复 dns01.env_file 的权限、内容或引用的凭据文件后重新执行。")
		default:
			detail := strings.TrimSpace(inputs.DNSCredentialsDetail)
			if detail == "" {
				detail = "DNS-01 provider env_file 已通过校验"
			}
			add("dns01-credentials", StatusPass, detail)
		}
	}

	if cfg.Mode() == appconfig.ModeListen {
		if inputs.ServiceBinaryOK {
			add("service-binary", StatusPass, "业务二进制存在且可执行")
		} else {
			path := inputs.ServiceBinaryPath
			if path == "" {
				path = cfg.ServiceBinary()
			}
			add("service-binary", StatusFail, "业务二进制不存在或不可执行: "+path, "先把业务二进制放到 service.exec_start 的绝对路径，并授予执行权限。")
		}
		if strings.TrimSpace(inputs.ServiceEnvFile) != "" {
			switch {
			case !inputs.ServiceEnvFileChecked:
				add("service-env-file", StatusFail, "service.env_file 未完成自动校验", "确认 service.env_file 存在、root-owned 且权限为 0600。")
			case !inputs.ServiceEnvFileReady:
				detail := strings.TrimSpace(inputs.ServiceEnvFileDetail)
				if detail == "" {
					detail = "service.env_file 不可用"
				} else {
					detail = "service.env_file 校验失败: " + detail
				}
				add("service-env-file", StatusFail, detail, "修复 service.env_file 的路径、属主或权限后重新执行。")
			default:
				detail := strings.TrimSpace(inputs.ServiceEnvFileDetail)
				if detail == "" {
					detail = "service.env_file 已通过 root-only 校验"
				}
				add("service-env-file", StatusPass, detail)
			}
		}
	}

	if inputs.TailscaleRequired {
		add("tailscale", StatusPass, "当前 app 需要 tailnet，deploy 将自动检查 Tailscale client")
	} else {
		add("tailscale", StatusPass, "当前 app 不需要 tailnet，跳过 Tailscale client 前置条件")
	}
	if strings.TrimSpace(inputs.TailscaleAuthKeyFile) != "" {
		switch {
		case !inputs.TailscaleAuthKeyFileChecked:
			add("tailscale-auth-key-file", StatusFail, "tailscale.auth_key_file 未完成自动校验", "确认 tailscale.auth_key_file 存在、root-owned、root-only，且只包含一个 preauth key。")
		case !inputs.TailscaleAuthKeyFileReady:
			detail := strings.TrimSpace(inputs.TailscaleAuthKeyFileDetail)
			if detail == "" {
				detail = "tailscale.auth_key_file 不可用"
			} else {
				detail = "tailscale.auth_key_file 校验失败: " + detail
			}
			add("tailscale-auth-key-file", StatusFail, detail, "修复 tailscale.auth_key_file 的路径、属主、权限、父目录或 token 内容后重新执行。")
		default:
			detail := strings.TrimSpace(inputs.TailscaleAuthKeyFileDetail)
			if detail == "" {
				detail = "tailscale.auth_key_file 已通过 root-only 校验"
			}
			add("tailscale-auth-key-file", StatusPass, detail)
		}
	}

	return Report{Checks: checks}
}

func evaluateDNSProbe(cfg appconfig.Config, domain string, probe preflight.DNSProbe, ok bool) (Status, string, string) {
	if !ok || strings.TrimSpace(probe.LookupError) != "" || len(probe.ResolvedIPs) == 0 {
		if cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01 {
			return StatusWarn, fmt.Sprintf("%s 的 DNS 解析未确认；DNS-01 模式不因此阻断部署", domain), "确认 dns01.provider/env_file 覆盖该域名，并在切流前把 app 域名解析到当前云服务器。"
		}
		return StatusFail, fmt.Sprintf("%s 的 DNS 解析未确认", domain), "修正 app.domains 的公开 DNS 后重新执行。"
	}
	public := publicRoutableIPs(probe.ResolvedIPs)
	if len(public) == 0 {
		if cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01 {
			return StatusWarn, fmt.Sprintf("%s 未解析到公网可路由地址: %s；DNS-01 模式不因此阻断部署", domain, strings.Join(probe.ResolvedIPs, ", ")), "确认 DNS-01 provider/env_file 覆盖该域名，并在切流前将 app 域名解析到当前云服务器。"
		}
		return StatusFail, fmt.Sprintf("%s 未解析到公网可路由地址: %s", domain, strings.Join(probe.ResolvedIPs, ", ")), "将 app 域名解析到当前云服务器的公网 A 或 AAAA 地址。"
	}
	hasExpectedAddress := strings.TrimSpace(probe.ExpectedIPv4) != "" || strings.TrimSpace(probe.ExpectedIPv6) != ""
	if !hasExpectedAddress {
		if cfg.App.ACMEChallenge == appconfig.ACMEChallengeHTTP01 {
			return StatusFail, fmt.Sprintf("%s 已解析到公网地址 %s，但未能确认这些地址属于当前云服务器", domain, strings.Join(public, ", ")), "确保当前主机可访问公网 IP 探测服务，或在主 meshify.yaml 的 advanced.network.public_ipv4/public_ipv6 中记录当前云服务器公网地址后重新执行。"
		}
		return StatusWarn, fmt.Sprintf("%s 已解析到公网地址 %s，但未能确认这些地址是否属于当前云服务器", domain, strings.Join(public, ", ")), "如需自动确认 DNS 指向当前云服务器，请在主 meshify.yaml 的 advanced.network.public_ipv4 或 public_ipv6 中记录本机公网地址。"
	}
	missing := []string{}
	if expectedIPv4 := strings.TrimSpace(probe.ExpectedIPv4); expectedIPv4 != "" && !containsString(probe.ResolvedIPs, expectedIPv4) {
		missing = append(missing, "IPv4 "+expectedIPv4)
	}
	if expectedIPv6 := strings.TrimSpace(probe.ExpectedIPv6); expectedIPv6 != "" && !containsString(probe.ResolvedIPs, expectedIPv6) {
		missing = append(missing, "IPv6 "+expectedIPv6)
	}
	if len(missing) > 0 {
		if cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01 {
			return StatusWarn, fmt.Sprintf("%s 的 DNS 结果缺少期望地址: %s；DNS-01 模式不因此阻断部署", domain, strings.Join(missing, ", ")), "确认 DNS-01 provider/env_file 覆盖该域名，并在切流前将 app 域名解析到当前云服务器的期望公网地址。"
		}
		return StatusFail, fmt.Sprintf("%s 的 DNS 结果缺少期望地址: %s", domain, strings.Join(missing, ", ")), "将 app 域名解析到当前云服务器的期望公网地址。"
	}
	return StatusPass, fmt.Sprintf("%s 已解析到公网地址 %s", domain, strings.Join(public, ", ")), ""
}

func addPortChecks(checks *[]Check, ports []preflight.PortBinding) {
	add := func(id string, status Status, summary string, remediations ...string) {
		*checks = append(*checks, Check{ID: id, Status: status, Summary: summary, Remediations: compact(remediations)})
	}
	if len(ports) == 0 {
		add("ports", StatusFail, "未能确认 80/tcp 和 443/tcp 端口占用状态", "确认 host 上可执行 ss，并重新运行 sudo meshify app deploy。")
		return
	}
	for _, required := range []int{80, 443} {
		binding, ok := findPortBinding(ports, required, "tcp")
		if !ok {
			add(fmt.Sprintf("port:%d/tcp", required), StatusFail, fmt.Sprintf("缺少 %d/tcp 端口占用探测结果", required), "重新运行 sudo meshify app deploy，确认端口占用探测完整。")
			continue
		}
		if !binding.InUse {
			add(fmt.Sprintf("port:%d/tcp", required), StatusPass, fmt.Sprintf("%d/tcp 当前可用", required))
			continue
		}
		process := strings.TrimSpace(binding.Process)
		if process == "" {
			process = "未知进程"
		}
		if strings.EqualFold(process, "nginx") {
			add(fmt.Sprintf("port:%d/tcp", required), StatusWarn, fmt.Sprintf("%d/tcp 已由 Nginx 使用，将复用 Nginx 虚拟主机", required), "确认现有 Nginx 站点没有占用 app.domains。")
			continue
		}
		add(fmt.Sprintf("port:%d/tcp", required), StatusFail, fmt.Sprintf("%d/tcp 已被 %s 占用，不能由 Meshify 管理的 Nginx 接管", required, process), "停止或迁移占用 80/443 的非 Nginx 服务后重新执行。")
	}
}

func findPortBinding(bindings []preflight.PortBinding, port int, protocol string) (preflight.PortBinding, bool) {
	for _, binding := range bindings {
		bindingProtocol := strings.ToLower(strings.TrimSpace(binding.Protocol))
		if bindingProtocol == "" {
			bindingProtocol = "tcp"
		}
		if binding.Port == port && bindingProtocol == strings.ToLower(protocol) {
			return binding, true
		}
	}
	return preflight.PortBinding{}, false
}

func publicRoutableIPs(values []string) []string {
	var public []string
	for _, value := range values {
		if isPublicRoutableIP(value) {
			public = append(public, strings.TrimSpace(value))
		}
	}
	return public
}

func isPublicRoutableIP(value string) bool {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublicRoutableIPPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var nonPublicRoutableIPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func containsString(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func (report Report) FailedCount() int {
	count := 0
	for _, check := range report.Checks {
		if check.Status == StatusFail {
			count++
		}
	}
	return count
}

func (report Report) Summary() string {
	if report.FailedCount() > 0 {
		return fmt.Sprintf("app deploy 预检发现 %d 个失败项", report.FailedCount())
	}
	return "app deploy 预检通过"
}

func (report Report) NextSteps() []string {
	seen := map[string]struct{}{}
	var steps []string
	for _, check := range report.Checks {
		for _, remediation := range check.Remediations {
			if _, ok := seen[remediation]; ok {
				continue
			}
			seen[remediation] = struct{}{}
			steps = append(steps, remediation)
		}
	}
	return steps
}

func compact(values []string) []string {
	var out []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}
