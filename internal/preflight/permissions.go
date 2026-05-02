package preflight

import "fmt"

type PermissionState struct {
	User          string `json:"user,omitempty"`
	IsRoot        bool   `json:"is_root"`
	SudoInstalled bool   `json:"sudo_installed"`
	SudoWorks     bool   `json:"sudo_works"`
}

func CheckPermissions(state PermissionState) CheckResult {
	findings := []string{}
	if state.User != "" {
		findings = append(findings, fmt.Sprintf("Current user: %s.", state.User))
	}

	switch {
	case state.IsRoot:
		findings = append(findings, "Privileged operations can run directly as root.")
		return newCheckResult("permissions", "Root or sudo", StatusPass, SeverityInfo, "Running with root privileges.", findings, nil)
	case state.SudoWorks:
		findings = append(findings, "sudo elevation is available for privileged steps.")
		return newCheckResult("permissions", "Root or sudo", StatusPass, SeverityInfo, "Current user can elevate with sudo.", findings, nil)
	case state.SudoInstalled:
		findings = append(findings, "sudo is installed but non-interactive elevation has not been confirmed.")
		return newCheckResult(
			"permissions",
			"Root or sudo",
			StatusFail,
			SeverityError,
			"meshify needs root or verified sudo access before deploy can continue.",
			findings,
			[]string{
				"Run the deploy command as root, or grant the current user working sudo access.",
				"Verify sudo with 'sudo -n true' before retrying the deploy workflow.",
			},
		)
	default:
		findings = append(findings, "Neither root execution nor sudo access was detected.")
		return newCheckResult(
			"permissions",
			"Root or sudo",
			StatusFail,
			SeverityError,
			"meshify needs root or sudo access before deploy can continue.",
			findings,
			[]string{"Run the deploy command as root, or install and configure sudo for the current user."},
		)
	}
}
