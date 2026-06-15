package appsvc

import (
	"fmt"
	"strings"
)

func ManagedMarker(appName string) string {
	return "Lanpanel-managed: app.name=" + strings.TrimSpace(appName)
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
		if strings.HasPrefix(normalized, "Lanpanel-managed:") {
			return fmt.Errorf("target file is managed by a different Lanpanel app")
		}
	}
	if foundCurrent {
		return nil
	}
	return fmt.Errorf("target file is not a Lanpanel-managed app file")
}
