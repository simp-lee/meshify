package tailscale

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const MarkerPath = "/var/lib/meshify/tailscale-client.json"

type Status struct {
	BackendState string
	Online       bool
	LoggedIn     bool
}

type Marker struct {
	LoginServer  string `json:"login_server"`
	AcceptDNS    bool   `json:"accept_dns"`
	AcceptRoutes bool   `json:"accept_routes"`
	ShieldsUp    bool   `json:"shields_up"`
	Hostname     string `json:"hostname,omitempty"`
	ManagedBy    string `json:"managed_by"`
}

type Prefs struct {
	ControlURL string
	RouteAll   bool
	CorpDNS    bool
	ShieldsUp  bool
}

func ParseStatusJSON(data []byte) (Status, error) {
	var raw struct {
		BackendState string `json:"BackendState"`
		Self         *struct {
			Online bool `json:"Online"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Status{}, fmt.Errorf("parse tailscale status json: %w", err)
	}
	status := Status{
		BackendState: strings.TrimSpace(raw.BackendState),
	}
	if raw.Self != nil {
		status.LoggedIn = true
		status.Online = raw.Self.Online
	}
	return status, nil
}

func ParsePrefsJSON(data []byte) (Prefs, error) {
	var raw struct {
		ControlURL string `json:"ControlURL"`
		RouteAll   bool   `json:"RouteAll"`
		CorpDNS    bool   `json:"CorpDNS"`
		ShieldsUp  bool   `json:"ShieldsUp"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Prefs{}, fmt.Errorf("parse tailscale prefs json: %w", err)
	}
	return Prefs{
		ControlURL: normalizeControlURL(raw.ControlURL),
		RouteAll:   raw.RouteAll,
		CorpDNS:    raw.CorpDNS,
		ShieldsUp:  raw.ShieldsUp,
	}, nil
}

func ParseMarker(data []byte) (Marker, error) {
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return Marker{}, fmt.Errorf("parse tailscale marker: %w", err)
	}
	marker.LoginServer = strings.TrimSpace(marker.LoginServer)
	marker.Hostname = strings.TrimSpace(marker.Hostname)
	marker.ManagedBy = strings.TrimSpace(marker.ManagedBy)
	return marker, nil
}

func (marker Marker) Matches(loginServer string, hostname string) bool {
	requestedHostname := strings.TrimSpace(hostname)
	hostnameMatches := requestedHostname == "" || marker.Hostname == requestedHostname
	return marker.ManagedBy == "meshify" &&
		normalizeControlURL(marker.LoginServer) == normalizeControlURL(loginServer) &&
		hostnameMatches &&
		!marker.AcceptDNS &&
		!marker.AcceptRoutes &&
		marker.ShieldsUp
}

func NewMarker(loginServer string, hostname string) Marker {
	return Marker{
		LoginServer:  strings.TrimSpace(loginServer),
		AcceptDNS:    false,
		AcceptRoutes: false,
		ShieldsUp:    true,
		Hostname:     strings.TrimSpace(hostname),
		ManagedBy:    "meshify",
	}
}

func (prefs Prefs) MatchesMeshifyPolicy() bool {
	return !prefs.RouteAll && !prefs.CorpDNS && prefs.ShieldsUp
}

func (prefs Prefs) PolicySummary() string {
	return fmt.Sprintf("accept_dns=%t accept_routes=%t shields_up=%t", prefs.CorpDNS, prefs.RouteAll, prefs.ShieldsUp)
}

func normalizeControlURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsedURL, err := url.Parse(value)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Hostname() == "" {
		return value
	}
	if parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return value
	}
	if parsedURL.Path != "" && parsedURL.Path != "/" {
		return value
	}
	scheme := strings.ToLower(parsedURL.Scheme)
	host := strings.ToLower(strings.TrimSuffix(parsedURL.Hostname(), "."))
	port := parsedURL.Port()
	if port != "" && port != "443" {
		return scheme + "://" + parsedURL.Host
	}
	return scheme + "://" + host
}
