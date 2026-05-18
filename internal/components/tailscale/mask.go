package tailscale

import "strings"

const redacted = "<redacted>"

func Mask(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return redacted
	}
	return value[:4] + redacted + value[len(value)-4:]
}

func MaskText(text string, secrets ...string) string {
	masked := text
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		masked = strings.ReplaceAll(masked, secret, Mask(secret))
	}
	return masked
}
