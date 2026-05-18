package appsvc

import (
	"fmt"
	"strings"
)

func ManagedMarker(appName string) string {
	return "Meshify-managed: app.name=" + strings.TrimSpace(appName)
}

func CheckManagedContent(appName string, content []byte) error {
	text := string(content)
	marker := ManagedMarker(appName)
	foundCurrent := false
	for _, line := range strings.Split(text, "\n") {
		normalized := strings.TrimSpace(line)
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "#"))
		if normalized == marker {
			foundCurrent = true
			continue
		}
		if strings.HasPrefix(normalized, "Meshify-managed:") {
			return fmt.Errorf("target file is managed by a different Meshify app")
		}
	}
	if foundCurrent {
		return nil
	}
	return fmt.Errorf("target file is not a Meshify-managed app file")
}
