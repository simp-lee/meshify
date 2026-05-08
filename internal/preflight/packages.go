package preflight

import (
	"fmt"
	"meshify/internal/config"
	"strings"
)

type PackageSourceState struct {
	Mode                    string `json:"mode,omitempty"`
	Version                 string `json:"version,omitempty"`
	URL                     string `json:"url,omitempty"`
	FilePath                string `json:"file_path,omitempty"`
	ExpectedSHA256          string `json:"expected_sha256,omitempty"`
	ReachabilityChecked     bool   `json:"reachability_checked"`
	Reachable               bool   `json:"reachable"`
	ReachabilityDetail      string `json:"reachability_detail,omitempty"`
	IntegrityChecked        bool   `json:"integrity_checked"`
	FileExists              bool   `json:"file_exists"`
	ActualSHA256            string `json:"actual_sha256,omitempty"`
	LegoMode                string `json:"lego_mode,omitempty"`
	LegoVersion             string `json:"lego_version,omitempty"`
	LegoURL                 string `json:"lego_url,omitempty"`
	LegoFilePath            string `json:"lego_file_path,omitempty"`
	LegoFileExists          bool   `json:"lego_file_exists"`
	LegoExpectedSHA256      string `json:"lego_expected_sha256,omitempty"`
	LegoReachabilityChecked bool   `json:"lego_reachability_checked"`
	LegoReachable           bool   `json:"lego_reachable"`
	LegoReachabilityDetail  string `json:"lego_reachability_detail,omitempty"`
	LegoIntegrityChecked    bool   `json:"lego_integrity_checked"`
	LegoActualSHA256        string `json:"lego_actual_sha256,omitempty"`
}

func CheckPackageSource(state PackageSourceState) CheckResult {
	mode := strings.TrimSpace(state.Mode)
	version := strings.TrimSpace(state.Version)
	findings := []string{}
	if version != "" {
		findings = append(findings, fmt.Sprintf("Pinned Headscale version: %s.", version))
	}
	if legoVersion := strings.TrimSpace(state.LegoVersion); legoVersion != "" {
		findings = append(findings, fmt.Sprintf("Pinned lego version: %s.", legoVersion))
	}
	if detail := strings.TrimSpace(state.ReachabilityDetail); detail != "" {
		findings = append(findings, fmt.Sprintf("Headscale package reachability detail: %s.", detail))
	}
	if detail := strings.TrimSpace(state.LegoReachabilityDetail); detail != "" {
		findings = append(findings, fmt.Sprintf("lego archive reachability detail: %s.", detail))
	}

	if mode == "" || version == "" {
		return newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Headscale source mode and version must be set before preflight can continue.",
			findings,
			[]string{"Set advanced.headscale_source.mode and advanced.headscale_source.version before deploy."},
		)
	}
	if mode != config.PackageSourceModeDirect && mode != config.PackageSourceModeMirror && mode != config.PackageSourceModeOffline {
		return newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			fmt.Sprintf("Unsupported Headscale source mode %q.", mode),
			findings,
			[]string{"Use direct, mirror, or offline as the Headscale source mode."},
		)
	}

	if updatedFindings, result := checkLegoArchiveSource(state, findings); result.ID != "" {
		return result
	} else {
		findings = updatedFindings
	}

	switch mode {
	case config.PackageSourceModeDirect:
		if !state.ReachabilityChecked {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Official Headscale package source reachability could not be confirmed from this host.",
				findings,
				[]string{"Fix host egress or package source access until meshify can reach the official Headscale package source before deploy."},
			)
		}
		if !state.Reachable {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"The official Headscale package source is unreachable from this host.",
				findings,
				[]string{"Switch to a reachable mirror, configure a proxy, or prepare an offline package before deploy."},
			)
		}
		if strings.TrimSpace(state.ExpectedSHA256) == "" {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Official Headscale package integrity evidence is missing.",
				findings,
				[]string{"Record the expected official package SHA-256 digest for the pinned Headscale version before deploy."},
			)
		}
		if !state.IntegrityChecked {
			findings = append(findings, fmt.Sprintf("Expected SHA-256: %s.", state.ExpectedSHA256))
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Official Headscale package integrity could not be verified automatically.",
				findings,
				[]string{"Fix checksum lookup or package download access until meshify can verify the official package SHA-256 digest before deploy."},
			)
		}
		if !sha256Matches(state.ExpectedSHA256, state.ActualSHA256) {
			findings = append(findings, fmt.Sprintf("Expected SHA-256: %s.", state.ExpectedSHA256))
			findings = append(findings, fmt.Sprintf("Actual SHA-256: %s.", strings.TrimSpace(state.ActualSHA256)))
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Official Headscale package integrity check failed.",
				findings,
				[]string{"Replace the direct package artifact metadata or switch to a verified mirror/offline package before deploy."},
			)
		}
		return newCheckResult("package-source", "Package source", StatusPass, SeverityInfo, "The official Headscale package source is reachable and the package digest matches.", findings, nil)
	case config.PackageSourceModeMirror:
		if strings.TrimSpace(state.URL) == "" || strings.TrimSpace(state.ExpectedSHA256) == "" {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Headscale mirror mode requires a package URL and expected SHA-256 digest.",
				findings,
				[]string{"Set advanced.headscale_source.url and advanced.headscale_source.sha256 before deploy."},
			)
		}
		findings = append(findings, fmt.Sprintf("Headscale mirror package URL: %s.", state.URL))
		if !state.ReachabilityChecked {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Headscale mirror package reachability could not be confirmed from this host.",
				findings,
				[]string{"Fix host egress, proxy settings, or the mirror URL until meshify can reach the configured package source before deploy."},
			)
		}
		if !state.Reachable {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"The configured Headscale mirror package URL is unreachable.",
				findings,
				[]string{"Fix the mirror URL, configure a proxy, or switch to another Headscale source mode before deploy."},
			)
		}
		if !state.IntegrityChecked {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Headscale mirror package integrity could not be verified automatically.",
				findings,
				[]string{"Fix mirror download access until meshify can verify the mirror package SHA-256 digest before deploy."},
			)
		}
		if !sha256Matches(state.ExpectedSHA256, state.ActualSHA256) {
			findings = append(findings, fmt.Sprintf("Expected SHA-256: %s.", state.ExpectedSHA256))
			findings = append(findings, fmt.Sprintf("Actual SHA-256: %s.", strings.TrimSpace(state.ActualSHA256)))
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Headscale mirror package integrity check failed.",
				findings,
				[]string{"Replace the mirror package with the expected artifact before deploy."},
			)
		}
		return newCheckResult("package-source", "Package source", StatusPass, SeverityInfo, "Headscale mirror package source is reachable and the package digest matches.", findings, nil)
	case config.PackageSourceModeOffline:
		if strings.TrimSpace(state.FilePath) == "" || strings.TrimSpace(state.ExpectedSHA256) == "" {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Headscale offline mode requires a local package path and expected SHA-256 digest.",
				findings,
				[]string{"Set advanced.headscale_source.file_path and advanced.headscale_source.sha256 before deploy."},
			)
		}
		findings = append(findings, fmt.Sprintf("Offline Headscale package path: %s.", state.FilePath))
		if !state.FileExists {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"The offline Headscale package file is missing.",
				findings,
				[]string{"Copy the expected Headscale .deb file to the configured file path before deploy."},
			)
		}
		if !state.IntegrityChecked {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Offline Headscale package integrity could not be verified automatically.",
				findings,
				[]string{"Fix local package access until meshify can compute and verify the offline package SHA-256 digest before deploy."},
			)
		}
		if !sha256Matches(state.ExpectedSHA256, state.ActualSHA256) {
			findings = append(findings, fmt.Sprintf("Expected SHA-256: %s.", state.ExpectedSHA256))
			findings = append(findings, fmt.Sprintf("Actual SHA-256: %s.", strings.TrimSpace(state.ActualSHA256)))
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Offline Headscale package integrity check failed.",
				findings,
				[]string{"Replace the offline package with the expected artifact before deploy."},
			)
		}
		return newCheckResult("package-source", "Package source", StatusPass, SeverityInfo, "Offline Headscale package file exists and its digest matches.", findings, nil)
	}
	return CheckResult{}
}

func checkLegoArchiveSource(state PackageSourceState, findings []string) ([]string, CheckResult) {
	mode := strings.TrimSpace(state.LegoMode)
	if mode == "" {
		mode = config.PackageSourceModeDirect
	}
	if strings.TrimSpace(state.LegoVersion) == "" || strings.TrimSpace(state.LegoExpectedSHA256) == "" {
		return findings, newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Pinned lego artifact metadata is missing.",
			findings,
			[]string{"Confirm meshify can select the pinned lego release URL and SHA-256 digest before deploy."},
		)
	}

	findings = append(findings, fmt.Sprintf("Pinned lego archive source mode: %s.", mode))
	switch mode {
	case config.PackageSourceModeOffline:
		if strings.TrimSpace(state.LegoFilePath) == "" {
			return findings, newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Offline lego archive mode requires a local archive path.",
				findings,
				[]string{"Set advanced.lego_source.file_path to the local pinned lego archive before deploy."},
			)
		}
		findings = append(findings, fmt.Sprintf("Offline lego archive path: %s.", strings.TrimSpace(state.LegoFilePath)))
		if !state.LegoFileExists {
			return findings, newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"The offline lego archive file is missing.",
				findings,
				[]string{"Copy the expected lego archive to advanced.lego_source.file_path before deploy."},
			)
		}
		if !state.LegoIntegrityChecked {
			findings = append(findings, fmt.Sprintf("Expected lego SHA-256: %s.", state.LegoExpectedSHA256))
			return findings, newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Offline lego archive integrity could not be verified automatically.",
				findings,
				[]string{"Fix local lego archive access until meshify can compute and verify the pinned archive SHA-256 digest before deploy."},
			)
		}
		if !sha256Matches(state.LegoExpectedSHA256, state.LegoActualSHA256) {
			findings = append(findings, fmt.Sprintf("Expected lego SHA-256: %s.", state.LegoExpectedSHA256))
			findings = append(findings, fmt.Sprintf("Actual lego SHA-256: %s.", strings.TrimSpace(state.LegoActualSHA256)))
			return findings, newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Offline lego archive integrity check failed.",
				findings,
				[]string{"Replace the offline lego archive with the pinned artifact for the configured architecture before deploy."},
			)
		}
		return findings, CheckResult{}
	case config.PackageSourceModeDirect:
	default:
		return findings, newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			fmt.Sprintf("Unsupported lego archive source mode %q.", mode),
			findings,
			[]string{"Use direct or offline as the lego archive source mode."},
		)
	}

	if strings.TrimSpace(state.LegoURL) == "" {
		return findings, newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Pinned lego archive URL is missing.",
			findings,
			[]string{"Confirm meshify can select the pinned lego release URL before deploy."},
		)
	}
	findings = append(findings, fmt.Sprintf("Pinned lego archive URL: %s.", strings.TrimSpace(state.LegoURL)))
	if !state.LegoReachabilityChecked {
		return findings, newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Pinned lego archive reachability could not be confirmed from this host.",
			findings,
			[]string{"Fix host egress or proxy settings until meshify can reach the pinned lego GitHub release archive before deploy."},
		)
	}
	if !state.LegoReachable {
		return findings, newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"The pinned lego archive is unreachable from this host.",
			findings,
			[]string{"Configure a proxy or allow GitHub release access before deploy; meshify installs its own pinned lego binary."},
		)
	}
	if !state.LegoIntegrityChecked {
		findings = append(findings, fmt.Sprintf("Expected lego SHA-256: %s.", state.LegoExpectedSHA256))
		return findings, newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Pinned lego archive integrity could not be verified automatically.",
			findings,
			[]string{"Fix GitHub release download access until meshify can verify the pinned lego archive digest before deploy."},
		)
	}
	if !sha256Matches(state.LegoExpectedSHA256, state.LegoActualSHA256) {
		findings = append(findings, fmt.Sprintf("Expected lego SHA-256: %s.", state.LegoExpectedSHA256))
		findings = append(findings, fmt.Sprintf("Actual lego SHA-256: %s.", strings.TrimSpace(state.LegoActualSHA256)))
		return findings, newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Pinned lego archive integrity check failed.",
			findings,
			[]string{"Replace the downloaded lego archive source or update the pinned meshify release metadata before deploy."},
		)
	}
	return findings, CheckResult{}
}

func sha256Matches(expected string, actual string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	actual = strings.ToLower(strings.TrimSpace(actual))
	if expected == "" || actual == "" {
		return false
	}
	return expected == actual
}
