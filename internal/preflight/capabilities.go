package preflight

type HostCapabilityState struct {
	AptGetAvailable         bool   `json:"apt_get_available"`
	AptGetDetail            string `json:"apt_get_detail,omitempty"`
	DpkgAvailable           bool   `json:"dpkg_available"`
	DpkgDetail              string `json:"dpkg_detail,omitempty"`
	SystemctlAvailable      bool   `json:"systemctl_available"`
	SystemctlDetail         string `json:"systemctl_detail,omitempty"`
	SystemdRuntimeAvailable bool   `json:"systemd_runtime_available"`
	SystemdRuntimeDetail    string `json:"systemd_runtime_detail,omitempty"`
}

func CheckHostCapabilities(state HostCapabilityState) CheckResult {
	findings := capabilityFindings(state)
	if state.AptGetAvailable && state.DpkgAvailable && state.SystemctlAvailable && state.SystemdRuntimeAvailable {
		return newCheckResult(
			"host-capabilities",
			"Host capabilities",
			StatusPass,
			SeverityInfo,
			"Required Debian-family deployment capabilities are available.",
			findings,
			nil,
		)
	}

	return newCheckResult(
		"host-capabilities",
		"Host capabilities",
		StatusFail,
		SeverityError,
		"The host is missing required Debian-family deployment capabilities.",
		findings,
		[]string{
			"Run deploy on a booted Debian-family system with apt-get, dpkg, and systemd available.",
			"Use a full VM or server instance rather than a minimal container, chroot, or non-systemd environment.",
		},
	)
}

func capabilityFindings(state HostCapabilityState) []string {
	findings := []string{}
	findings = append(findings, capabilityFinding("apt-get", state.AptGetAvailable, state.AptGetDetail))
	findings = append(findings, capabilityFinding("dpkg", state.DpkgAvailable, state.DpkgDetail))
	findings = append(findings, capabilityFinding("systemctl", state.SystemctlAvailable, state.SystemctlDetail))
	findings = append(findings, capabilityFinding("systemd runtime", state.SystemdRuntimeAvailable, state.SystemdRuntimeDetail))
	return findings
}

func capabilityFinding(name string, available bool, detail string) string {
	if available {
		if detail == "" {
			return name + " is available."
		}
		return name + " is available: " + detail + "."
	}
	if detail == "" {
		return name + " is not available."
	}
	return name + " is not available: " + detail + "."
}
