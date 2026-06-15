package realip

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func RefreshIntervalDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("refresh_interval must be a positive Go duration such as 72h")
	}
	if duration%time.Second != 0 {
		return 0, fmt.Errorf("refresh_interval must resolve to whole seconds for systemd timer rendering")
	}
	return duration, nil
}

func SystemdRefreshInterval(value string) (string, error) {
	duration, err := RefreshIntervalDuration(value)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(int64(duration/time.Second), 10) + "s", nil
}
