package tls

import (
	"errors"
	"strings"
)

func ValidateReloadHook(content []byte) error {
	var errs []string
	lines := shellLines(content)
	if len(lines) == 0 || lines[0] != "#!/bin/sh" {
		errs = append(errs, "reload hook missing #!/bin/sh")
	}
	active := activeShellLines(lines)
	setIndex := lineIndex(active, "set -eu")
	testIndex := lineIndex(active, "nginx -t")
	systemctlIndex := lineIndex(active, "systemctl reload nginx")
	fallbackIndex := lineIndex(active, "nginx -s reload")
	if setIndex < 0 {
		errs = append(errs, "reload hook missing set -eu")
	}
	if testIndex < 0 {
		errs = append(errs, "reload hook missing nginx -t")
	}
	if systemctlIndex < 0 {
		errs = append(errs, "reload hook missing systemctl reload nginx")
	}
	if fallbackIndex < 0 {
		errs = append(errs, "reload hook missing nginx -s reload")
	}
	if setIndex >= 0 && testIndex >= 0 && setIndex > testIndex {
		errs = append(errs, "reload hook must enable set -eu before testing Nginx")
	}
	if testIndex >= 0 && systemctlIndex >= 0 && testIndex > systemctlIndex {
		errs = append(errs, "reload hook must run nginx -t before systemctl reload nginx")
	}
	if testIndex >= 0 && fallbackIndex >= 0 && testIndex > fallbackIndex {
		errs = append(errs, "reload hook must run nginx -t before nginx -s reload")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func shellLines(content []byte) []string {
	rawLines := strings.Split(string(content), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines
}

func activeShellLines(lines []string) []string {
	active := []string{}
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		active = append(active, line)
	}
	return active
}

func lineIndex(lines []string, want string) int {
	for index, line := range lines {
		if line == want {
			return index
		}
	}
	return -1
}
