package headscale

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

type ACLPolicy struct {
	Groups        map[string][]string `json:"groups"`
	Hosts         map[string]string   `json:"hosts"`
	TagOwners     map[string][]string `json:"tagOwners"`
	ACLs          []ACLRule           `json:"acls"`
	AutoApprovers AutoApprovers       `json:"autoApprovers"`
	SSH           []any               `json:"ssh"`
}

type ACLRule struct {
	Action string    `json:"action"`
	Proto  *Protocol `json:"proto"`
	Src    []string  `json:"src"`
	Dst    []string  `json:"dst"`
}

type Protocol string

func (r *ACLRule) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	filtered := make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		if !strings.HasPrefix(key, "#") {
			canonical, ok := canonicalJSONField(key, "action", "proto", "src", "dst")
			if !ok {
				filtered[key] = value
				continue
			}
			filtered[canonical] = value
		}
	}

	filteredData, err := json.Marshal(filtered)
	if err != nil {
		return err
	}

	type aclRule ACLRule
	var rule aclRule
	decoder := json.NewDecoder(bytes.NewReader(filteredData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rule); err != nil {
		return err
	}

	*r = ACLRule(rule)
	return nil
}

func (p *Protocol) UnmarshalJSON(data []byte) error {
	var raw any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}

	var value string
	switch v := raw.(type) {
	case string:
		value = strings.ToLower(v)
	case json.Number:
		value = v.String()
	default:
		return fmt.Errorf("invalid protocol %q: must be a known protocol name or valid protocol number 0-255", strings.TrimSpace(string(data)))
	}

	protocol := Protocol(value)
	if err := protocol.validate(); err != nil {
		return err
	}
	*p = protocol
	return nil
}

func (p Protocol) validate() error {
	switch p {
	case "", "icmp", "igmp", "ipv4", "ip-in-ip", "tcp", "egp", "igp", "udp", "gre", "esp", "ah", "sctp":
		return nil
	case "*":
		return fmt.Errorf(`proto name "*" not known; use protocol number 0-255 or protocol name (icmp, tcp, udp, etc.)`)
	default:
		str := string(p)
		if str == "0" || len(str) > 1 && str[0] == '0' {
			return fmt.Errorf("leading 0 not permitted in protocol number %q", str)
		}
		protocolNumber, err := strconv.Atoi(str)
		if err != nil {
			return fmt.Errorf("invalid protocol %q: must be a known protocol name or valid protocol number 0-255", p)
		}
		if protocolNumber < 0 || protocolNumber > 255 {
			return fmt.Errorf("protocol number %d out of range (0-255)", protocolNumber)
		}
		return nil
	}
}

type AutoApprovers struct {
	Routes   map[string][]string `json:"routes"`
	ExitNode []string            `json:"exitNode"`
}

func ParseACLPolicy(data []byte) (ACLPolicy, error) {
	data, err := hujson.Standardize(data)
	if err != nil {
		return ACLPolicy{}, err
	}

	policy, err := decodeACLPolicy(data)
	if err != nil {
		return ACLPolicy{}, err
	}
	return policy, nil
}

func decodeACLPolicy(data []byte) (ACLPolicy, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ACLPolicy{}, err
	}

	normalized := make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		canonical, ok := canonicalJSONField(key, "groups", "hosts", "tagOwners", "acls", "autoApprovers", "ssh")
		if !ok {
			normalized[key] = value
			continue
		}
		normalized[canonical] = value
	}

	normalizedData, err := json.Marshal(normalized)
	if err != nil {
		return ACLPolicy{}, err
	}

	var policy ACLPolicy
	decoder := json.NewDecoder(bytes.NewReader(normalizedData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return ACLPolicy{}, err
	}
	return policy, nil
}

func canonicalJSONField(field string, known ...string) (string, bool) {
	for _, candidate := range known {
		if strings.EqualFold(field, candidate) {
			return candidate, true
		}
	}
	return "", false
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
	if len(policy.Hosts) != 0 {
		errs = append(errs, "hosts must be empty in the default policy")
	}
	if len(policy.TagOwners) != 0 {
		errs = append(errs, "tagOwners must be empty in the default policy")
	}
	if len(policy.AutoApprovers.Routes) != 0 || len(policy.AutoApprovers.ExitNode) != 0 {
		errs = append(errs, "autoApprovers must be empty in the default policy")
	}
	if len(policy.ACLs) != 1 {
		errs = append(errs, "policy must contain exactly one allow-all ACL")
	} else {
		rule := policy.ACLs[0]
		if rule.Action != "accept" {
			errs = append(errs, "default ACL action must be accept")
		}
		if rule.Proto != nil {
			errs = append(errs, "default ACL proto must be omitted")
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

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
