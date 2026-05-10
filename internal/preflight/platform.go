package preflight

import (
	"bufio"
	"fmt"
	"strings"
)

const supportedServerMatrix = "Debian, Ubuntu, and Debian-family systems with apt/dpkg/systemd"

type PlatformInfo struct {
	ID         string `json:"id,omitempty"`
	IDLike     string `json:"id_like,omitempty"`
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
		case "ID_LIKE":
			info.IDLike = value
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
	idLike := strings.ToLower(strings.TrimSpace(info.IDLike))
	version := strings.TrimSpace(info.VersionID)
	label := platformLabel(info)

	if id == "" {
		return newCheckResult(
			"platform",
			"Supported platform",
			StatusFail,
			SeverityError,
			"Unable to determine the server distribution from os-release data.",
			[]string{"The server support matrix is limited to Debian, Ubuntu, and Debian-family systems."},
			[]string{"Confirm /etc/os-release is readable and reports ID=debian, ID=ubuntu, or ID_LIKE containing debian or ubuntu."},
		)
	}

	findings := []string{fmt.Sprintf("Supported server matrix: %s.", supportedServerMatrix)}
	if version != "" {
		findings = append(findings, fmt.Sprintf("Detected VERSION_ID: %s.", version))
	}
	if idLike != "" {
		findings = append(findings, fmt.Sprintf("Detected ID_LIKE: %s.", idLike))
	}

	if isSupportedPlatform(id, idLike) {
		return newCheckResult(
			"platform",
			"Supported platform",
			StatusPass,
			SeverityInfo,
			fmt.Sprintf("%s is inside the server support matrix.", label),
			findings,
			nil,
		)
	}

	return newCheckResult(
		"platform",
		"Supported platform",
		StatusFail,
		SeverityError,
		fmt.Sprintf("%s is outside the server support matrix.", label),
		findings,
		[]string{
			"Use Debian, Ubuntu, or a Debian-family distribution that reports debian or ubuntu in ID_LIKE.",
			"Treat non-Debian-family distributions as out of scope until a support-matrix change lands.",
		},
	)
}

func isSupportedPlatform(id string, idLike string) bool {
	for _, token := range platformFamilyTokens(id, idLike) {
		switch token {
		case "debian", "ubuntu":
			return true
		}
	}
	return false
}

func platformFamilyTokens(values ...string) []string {
	tokens := []string{}
	for _, value := range values {
		tokens = append(tokens, strings.Fields(strings.ToLower(strings.TrimSpace(value)))...)
	}
	return tokens
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
