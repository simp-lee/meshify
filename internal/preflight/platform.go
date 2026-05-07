package preflight

import (
	"bufio"
	"fmt"
	"strings"
)

const supportedServerMatrix = "Debian 13 and Ubuntu 24.04 LTS"

type PlatformInfo struct {
	ID         string `json:"id,omitempty"`
	VersionID  string `json:"version_id,omitempty"`
	PrettyName string `json:"pretty_name,omitempty"`
}

func ParseOSRelease(content string) PlatformInfo {
	info := PlatformInfo{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = parseOSReleaseValue(value)
		switch key {
		case "ID":
			info.ID = value
		case "VERSION_ID":
			info.VersionID = value
		case "PRETTY_NAME":
			info.PrettyName = value
		}
	}
	return info
}

func parseOSReleaseValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if quote := value[0]; quote == '"' || quote == '\'' {
		return parseQuotedOSReleaseValue(value[1:], quote)
	}

	return unescapeOSReleaseValue(value, 0)
}

func parseQuotedOSReleaseValue(value string, quote byte) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == quote {
			return builder.String()
		}
		if quote == '"' && character == '\\' && index+1 < len(value) {
			index++
			builder.WriteByte(value[index])
			continue
		}
		builder.WriteByte(character)
	}
	return builder.String()
}

func unescapeOSReleaseValue(value string, start int) string {
	var builder strings.Builder
	for index := start; index < len(value); index++ {
		character := value[index]
		if character == '\\' && index+1 < len(value) {
			index++
			builder.WriteByte(value[index])
			continue
		}
		builder.WriteByte(character)
	}
	return builder.String()
}

func CheckPlatform(info PlatformInfo) CheckResult {
	id := strings.ToLower(strings.TrimSpace(info.ID))
	version := strings.TrimSpace(info.VersionID)
	label := platformLabel(info)

	if id == "" || version == "" {
		return newCheckResult(
			"platform",
			"Supported platform",
			StatusFail,
			SeverityError,
			"Unable to determine the server distribution from os-release data.",
			[]string{"The first-release support matrix is limited to Debian 13 and Ubuntu 24.04 LTS."},
			[]string{"Confirm /etc/os-release is readable and reports a Debian 13 or Ubuntu 24.04 LTS host."},
		)
	}

	if isSupportedPlatform(id, version) {
		return newCheckResult(
			"platform",
			"Supported platform",
			StatusPass,
			SeverityInfo,
			fmt.Sprintf("%s is inside the first-release support matrix.", label),
			[]string{fmt.Sprintf("Supported server matrix: %s.", supportedServerMatrix)},
			nil,
		)
	}

	return newCheckResult(
		"platform",
		"Supported platform",
		StatusFail,
		SeverityError,
		fmt.Sprintf("%s is outside the first-release support matrix.", label),
		[]string{fmt.Sprintf("Supported server matrix: %s.", supportedServerMatrix)},
		[]string{
			"Use Debian 13 or Ubuntu 24.04 LTS for first-release server deployments.",
			"Treat other distributions as out of scope until a later support-matrix change lands.",
		},
	)
}

func isSupportedPlatform(id string, version string) bool {
	switch id {
	case "debian":
		return version == "13" || strings.HasPrefix(version, "13.")
	case "ubuntu":
		return version == "24.04" || strings.HasPrefix(version, "24.04.")
	default:
		return false
	}
}

func platformLabel(info PlatformInfo) string {
	if prettyName := strings.TrimSpace(info.PrettyName); prettyName != "" {
		return prettyName
	}
	parts := compactStrings([]string{strings.TrimSpace(info.ID), strings.TrimSpace(info.VersionID)})
	if len(parts) == 0 {
		return "the detected host"
	}
	return strings.Join(parts, " ")
}
