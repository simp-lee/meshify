package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAllowAllPolicyAcceptsDeployPolicy(t *testing.T) {
	t.Parallel()

	content := readDeployPolicy(t)
	if err := ValidateAllowAllPolicy(content); err != nil {
		t.Fatalf("ValidateAllowAllPolicy() error = %v", err)
	}
}

func TestValidateAllowAllPolicyRejectsNarrowOrExpandedPolicy(t *testing.T) {
	t.Parallel()

	content := string(readDeployPolicy(t))
	content = strings.Replace(content, `"dst": ["*:*"]`, `"dst": ["tag:ops:*"]`, 1)

	err := ValidateAllowAllPolicy([]byte(content))
	if err == nil {
		t.Fatal("ValidateAllowAllPolicy() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), `default ACL dst must be ["*:*"]`) {
		t.Fatalf("error = %q, want ACL dst failure", err.Error())
	}
}

func readDeployPolicy(t *testing.T) []byte {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "templates", "etc", "headscale", "policy.hujson"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return content
}
