package headscale

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

type ACLPolicy struct {
	Groups    map[string][]string `json:"groups"`
	TagOwners map[string][]string `json:"tagOwners"`
	ACLs      []ACLRule           `json:"acls"`
	SSH       []any               `json:"ssh"`
	Tests     []any               `json:"tests"`
}

type ACLRule struct {
	Action string   `json:"action"`
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
}

func ParseACLPolicy(data []byte) (ACLPolicy, error) {
	var policy ACLPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return ACLPolicy{}, err
	}
	return policy, nil
}

func ValidateAllowAllPolicy(data []byte) error {
	policy, err := ParseACLPolicy(data)
	if err != nil {
		return err
	}

	var errs []string
	if len(policy.Groups) != 0 {
		errs = append(errs, "groups must be empty in the default policy")
	}
	if len(policy.TagOwners) != 0 {
		errs = append(errs, "tagOwners must be empty in the default policy")
	}
	if len(policy.ACLs) != 1 {
		errs = append(errs, "policy must contain exactly one allow-all ACL")
	} else {
		rule := policy.ACLs[0]
		if rule.Action != "accept" {
			errs = append(errs, "default ACL action must be accept")
		}
		if !slices.Equal(rule.Src, []string{"*"}) {
			errs = append(errs, `default ACL src must be ["*"]`)
		}
		if !slices.Equal(rule.Dst, []string{"*:*"}) {
			errs = append(errs, `default ACL dst must be ["*:*"]`)
		}
	}
	if len(policy.SSH) != 0 {
		errs = append(errs, "ssh policy must be empty")
	}
	if len(policy.Tests) != 0 {
		errs = append(errs, "policy tests must be empty")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
