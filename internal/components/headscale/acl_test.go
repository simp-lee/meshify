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

func TestValidateAllowAllPolicyAcceptsHuJSONPolicy(t *testing.T) {
	t.Parallel()

	content := []byte(`{
  // Headscale policy files are HuJSON, so comments are valid.
  "groups": {},
  "tagOwners": {},
  "acls": [
    {
      "action": "accept",
      "src": ["*"],
      "dst": ["*:*"],
    },
  ],
  "ssh": [],
}`)

	if err := ValidateAllowAllPolicy(content); err != nil {
		t.Fatalf("ValidateAllowAllPolicy() error = %v", err)
	}
}

func TestParseACLPolicyAcceptsHeadscaleV028PolicyFields(t *testing.T) {
	t.Parallel()

	content := []byte(`{
  "groups": {
    "group:ops": ["ops@"],
  },
  "hosts": {
    "router.internal": "10.20.0.1/32",
  },
  "tagOwners": {
    "tag:router": ["group:ops"],
  },
  "acls": [
    {
      "action": "accept",
      "proto": "tcp",
      "src": ["group:ops"],
      "dst": ["router.internal:22"],
    },
  ],
  "autoApprovers": {
    "routes": {
      "10.20.0.0/16": ["tag:router"],
    },
    "exitNode": ["group:ops"],
  },
  "ssh": [],
}`)

	policy, err := ParseACLPolicy(content)
	if err != nil {
		t.Fatalf("ParseACLPolicy() error = %v", err)
	}
	if policy.Hosts["router.internal"] != "10.20.0.1/32" {
		t.Fatalf("Hosts = %#v, want Headscale hosts field parsed", policy.Hosts)
	}
	if policy.ACLs[0].Proto == nil || *policy.ACLs[0].Proto != "tcp" {
		t.Fatalf("ACL proto = %#v, want tcp", policy.ACLs[0].Proto)
	}
	if got := policy.AutoApprovers.Routes["10.20.0.0/16"]; len(got) != 1 || got[0] != "tag:router" {
		t.Fatalf("AutoApprovers.Routes = %#v", policy.AutoApprovers.Routes)
	}
	if got := policy.AutoApprovers.ExitNode; len(got) != 1 || got[0] != "group:ops" {
		t.Fatalf("AutoApprovers.ExitNode = %#v", got)
	}
}

func TestParseACLPolicyMatchesHeadscaleCaseInsensitiveFields(t *testing.T) {
	t.Parallel()

	content := []byte(`{
  "Groups": {},
  "Hosts": {},
  "TagOwners": {},
  "ACLs": [
    {
      "#metadata": {"owner": "headscale-admin"},
      "Action": "accept",
      "Proto": "TCP",
      "SRC": ["*"],
      "DST": ["*:*"],
    },
  ],
  "AutoApprovers": {},
  "SSH": [],
}`)

	policy, err := ParseACLPolicy(content)
	if err != nil {
		t.Fatalf("ParseACLPolicy() error = %v", err)
	}
	if policy.ACLs[0].Proto == nil || *policy.ACLs[0].Proto != "tcp" {
		t.Fatalf("ACL proto = %#v, want lowercase tcp", policy.ACLs[0].Proto)
	}
	if !slicesEqual(policy.ACLs[0].Src, []string{"*"}) || !slicesEqual(policy.ACLs[0].Dst, []string{"*:*"}) {
		t.Fatalf("ACL = %#v, want case-insensitive src/dst parsed", policy.ACLs[0])
	}
}

func TestParseACLPolicyRejectsInvalidProtoLikeHeadscale(t *testing.T) {
	t.Parallel()

	for name, proto := range map[string]string{
		"boolean":      `true`,
		"wildcard":     `"*"`,
		"zero":         `"0"`,
		"leading zero": `"006"`,
		"unknown name": `"not-a-proto"`,
		"out of range": `"256"`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			content := []byte(`{
  "groups": {},
  "tagOwners": {},
  "acls": [
    {
      "action": "accept",
      "proto": ` + proto + `,
      "src": ["*"],
      "dst": ["*:*"],
    },
  ],
  "ssh": [],
}`)

			err := ValidateAllowAllPolicy(content)
			if err == nil {
				t.Fatal("ValidateAllowAllPolicy() error = nil, want proto validation failure")
			}
		})
	}
}

func TestParseACLPolicyIgnoresACLCommentFields(t *testing.T) {
	t.Parallel()

	content := []byte(`{
  "groups": {},
  "tagOwners": {},
  "acls": [
    {
      "#id": "allow-all",
      "#metadata": {"owner": "headscale-admin"},
      "action": "accept",
      "src": ["*"],
      "dst": ["*:*"],
    },
  ],
  "ssh": [],
}`)

	if err := ValidateAllowAllPolicy(content); err != nil {
		t.Fatalf("ValidateAllowAllPolicy() error = %v", err)
	}
}

func TestParseACLPolicyRejectsACLUnknownFields(t *testing.T) {
	t.Parallel()

	content := []byte(`{
  "groups": {},
  "tagOwners": {},
  "acls": [
    {
      "action": "accept",
      "src": ["*"],
      "dst": ["*:*"],
      "tests": [],
    },
  ],
  "ssh": [],
}`)

	err := ValidateAllowAllPolicy(content)
	if err == nil {
		t.Fatal("ValidateAllowAllPolicy() error = nil, want unknown ACL field failure")
	}
	if !strings.Contains(err.Error(), `unknown field "tests"`) {
		t.Fatalf("error = %q, want ACL unknown field failure", err.Error())
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

func TestValidateAllowAllPolicyRejectsNonDefaultHeadscaleV028Fields(t *testing.T) {
	t.Parallel()

	for name, replacement := range map[string]struct {
		replacement string
		want        string
	}{
		"hosts": {
			replacement: `"tagOwners": {}, "hosts": {"router.internal": "10.20.0.1/32"}`,
			want:        "hosts must be empty",
		},
		"proto": {
			replacement: `"action": "accept", "proto": "tcp"`,
			want:        "default ACL proto must be omitted",
		},
		"autoApprovers": {
			replacement: `"ssh": [], "autoApprovers": {"exitNode": ["group:ops"]}`,
			want:        "autoApprovers must be empty",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			content := string(readDeployPolicy(t))
			switch name {
			case "hosts":
				content = strings.Replace(content, `"tagOwners": {}`, replacement.replacement, 1)
			case "proto":
				content = strings.Replace(content, `"action": "accept"`, replacement.replacement, 1)
			case "autoApprovers":
				content = strings.Replace(content, `"ssh": []`, replacement.replacement, 1)
			}

			err := ValidateAllowAllPolicy([]byte(content))
			if err == nil {
				t.Fatal("ValidateAllowAllPolicy() error = nil, want non-default field failure")
			}
			if !strings.Contains(err.Error(), replacement.want) {
				t.Fatalf("error = %q, want %q", err.Error(), replacement.want)
			}
		})
	}
}

func TestValidateAllowAllPolicyRejectsHeadscaleUnknownFields(t *testing.T) {
	t.Parallel()

	content := string(readDeployPolicy(t))
	content = strings.Replace(content, `"ssh": []`, `"ssh": [], "tests": []`, 1)

	err := ValidateAllowAllPolicy([]byte(content))
	if err == nil {
		t.Fatal("ValidateAllowAllPolicy() error = nil, want unknown field failure")
	}
	if !strings.Contains(err.Error(), `unknown field "tests"`) {
		t.Fatalf("error = %q, want Headscale policy unknown field failure", err.Error())
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

func slicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
