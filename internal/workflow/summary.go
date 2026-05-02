package workflow

import "strings"

func JoinStepKeys(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}
