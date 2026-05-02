package verify

import "strings"

func SummarizeChecks(checks []Check) string {
	if len(checks) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		parts = append(parts, string(check.Status)+":"+check.ID)
	}
	return strings.Join(parts, ", ")
}
