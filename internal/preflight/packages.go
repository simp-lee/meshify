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
	LegoVersion             string `json:"lego_version,omitempty"`
	LegoURL                 string `json:"lego_url,omitempty"`
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
			"Package source mode and version must be set before preflight can continue.",
			findings,
			[]string{"Set advanced.package_source.mode and advanced.package_source.version before deploy."},
		)
	}
	if mode != config.PackageSourceModeDirect && mode != config.PackageSourceModeMirror && mode != config.PackageSourceModeOffline {
		return newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			fmt.Sprintf("Unsupported package source mode %q.", mode),
			findings,
			[]string{"Use direct, mirror, or offline as the package source mode."},
		)
	}

	if result := checkLegoArchiveSource(state, findings); result.ID != "" {
		return result
	}

	switch mode {
	case config.PackageSourceModeDirect:
		if !state.ReachabilityChecked {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Official package source reachability could not be confirmed from this host.",
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
				"Official package integrity evidence is missing.",
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
				"Official package integrity could not be verified automatically.",
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
				"Official package integrity check failed.",
				findings,
				[]string{"Replace the direct package artifact metadata or switch to a verified mirror/offline package before deploy."},
			)
		}
		return newCheckResult("package-source", "Package source", StatusPass, SeverityInfo, "The official package source is reachable and the package digest matches.", findings, nil)
	case config.PackageSourceModeMirror:
		if strings.TrimSpace(state.URL) == "" || strings.TrimSpace(state.ExpectedSHA256) == "" {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Mirror mode requires a package URL and expected SHA-256 digest.",
				findings,
				[]string{"Set advanced.package_source.url and advanced.package_source.sha256 before deploy."},
			)
		}
		findings = append(findings, fmt.Sprintf("Mirror package URL: %s.", state.URL))
		if !state.ReachabilityChecked {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Mirror package reachability could not be confirmed from this host.",
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
				"The configured mirror package URL is unreachable.",
				findings,
				[]string{"Fix the mirror URL, configure a proxy, or switch to another package source mode before deploy."},
			)
		}
		if !state.IntegrityChecked {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Mirror package integrity could not be verified automatically.",
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
				"Mirror package integrity check failed.",
				findings,
				[]string{"Replace the mirror package with the expected artifact before deploy."},
			)
		}
		return newCheckResult("package-source", "Package source", StatusPass, SeverityInfo, "Mirror package source is reachable and the package digest matches.", findings, nil)
	case config.PackageSourceModeOffline:
		if strings.TrimSpace(state.FilePath) == "" || strings.TrimSpace(state.ExpectedSHA256) == "" {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"Offline mode requires a local package path and expected SHA-256 digest.",
				findings,
				[]string{"Set advanced.package_source.file_path and advanced.package_source.sha256 before deploy."},
			)
		}
		findings = append(findings, fmt.Sprintf("Offline package path: %s.", state.FilePath))
		if !state.FileExists {
			return newCheckResult(
				"package-source",
				"Package source",
				StatusFail,
				SeverityError,
				"The offline package file is missing.",
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
				"Offline package integrity could not be verified automatically.",
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
				"Offline package integrity check failed.",
				findings,
				[]string{"Replace the offline package with the expected artifact before deploy."},
			)
		}
		return newCheckResult("package-source", "Package source", StatusPass, SeverityInfo, "Offline package file exists and its digest matches.", findings, nil)
	}
	return CheckResult{}
}

func checkLegoArchiveSource(state PackageSourceState, findings []string) CheckResult {
	if strings.TrimSpace(state.LegoVersion) == "" || strings.TrimSpace(state.LegoURL) == "" || strings.TrimSpace(state.LegoExpectedSHA256) == "" {
		return newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Pinned lego artifact metadata is missing.",
			findings,
			[]string{"Confirm meshify can select the pinned lego release URL and SHA-256 digest before deploy."},
		)
	}
	findings = append(findings, fmt.Sprintf("Pinned lego archive URL: %s.", strings.TrimSpace(state.LegoURL)))
	if !state.LegoReachabilityChecked {
		return newCheckResult(
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
		return newCheckResult(
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
		return newCheckResult(
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
		return newCheckResult(
			"package-source",
			"Package source",
			StatusFail,
			SeverityError,
			"Pinned lego archive integrity check failed.",
			findings,
			[]string{"Replace the downloaded lego archive source or update the pinned meshify release metadata before deploy."},
		)
	}
	return CheckResult{}
}

func sha256Matches(expected string, actual string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	actual = strings.ToLower(strings.TrimSpace(actual))
	if expected == "" || actual == "" {
		return false
	}
	return expected == actual
}
