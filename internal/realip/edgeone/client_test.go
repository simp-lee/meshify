package edgeone

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationFixture(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"ZoneId":"zone-2abcDEF123"}`)
	now := time.Unix(1700000000, 0).UTC()
	got, err := Authorization("AKIDEXAMPLE", "SECRETEXAMPLE", now, payload, Host)
	if err != nil {
		t.Fatalf("Authorization() error = %v", err)
	}
	want := "TC3-HMAC-SHA256 Credential=AKIDEXAMPLE/2023-11-14/teo/tc3_request, SignedHeaders=content-type;host, Signature=f7d01cf59e2e1a9d1b0292c8c7cceaea4106dbcd2806e770782210183dccd5d1"
	if got != want {
		t.Fatalf("Authorization() = %q, want %q", got, want)
	}
}

func TestAuthorizationRejectsMissingSigningInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		secretID  string
		secretKey string
		host      string
		want      string
	}{
		{name: "empty secret id", secretKey: "key", host: Host, want: "secret id is required"},
		{name: "empty secret key", secretID: "id", host: Host, want: "secret key is required"},
		{name: "empty host", secretID: "id", secretKey: "key", want: "host is required"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Authorization(tt.secretID, tt.secretKey, time.Unix(1700000000, 0).UTC(), []byte(`{}`), tt.host)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Authorization() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSignRequestRejectsMissingCredentials(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequest(http.MethodPost, Endpoint, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	err = SignRequest(request, Credentials{SecretID: "", SecretKey: "key"}, time.Unix(1700000000, 0).UTC(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "secret id is required") {
		t.Fatalf("SignRequest() error = %v, want secret id failure", err)
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization header = %q, want empty after signing failure", request.Header.Get("Authorization"))
	}
}

func TestDescribeOriginACLCallsOnlyDescribeAction(t *testing.T) {
	t.Parallel()

	var gotAction string
	var gotBody string
	var gotHost string
	var gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.Header.Get("X-TC-Action")
		gotHost = r.Host
		gotAuthorization = r.Header.Get("Authorization")
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		gotBody = string(data)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Response": map[string]any{
				"RequestId": "request-id",
				"OriginACLInfo": map[string]any{
					"Status":          "online",
					"L7Hosts":         []string{"app.example.com"},
					"OriginACLFamily": "global",
					"CurrentOriginACL": map[string]any{
						"Version": "v1",
						"EntireAddresses": map[string]any{
							"IPv4": []string{"8.8.8.8/32"},
							"IPv6": []string{},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	now := time.Unix(1700000000, 0).UTC()
	client := Client{Endpoint: server.URL, Now: func() time.Time { return now }}
	info, err := client.DescribeOriginACL(context.Background(), Credentials{SecretID: "id", SecretKey: "key"}, "zone-2abcDEF123")
	if err != nil {
		t.Fatalf("DescribeOriginACL() error = %v", err)
	}
	endpointURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test endpoint URL: %v", err)
	}
	if gotHost != endpointURL.Host {
		t.Fatalf("request Host = %q, want endpoint host %q", gotHost, endpointURL.Host)
	}
	wantAuthorization, err := Authorization("id", "key", now, []byte(`{"ZoneId":"zone-2abcDEF123"}`), endpointURL.Host)
	if err != nil {
		t.Fatalf("Authorization() error = %v", err)
	}
	if gotAuthorization != wantAuthorization {
		t.Fatalf("Authorization header = %q, want signature for endpoint host %q", gotAuthorization, endpointURL.Host)
	}
	if gotAction != ActionDescribeOriginACL {
		t.Fatalf("X-TC-Action = %q, want %q", gotAction, ActionDescribeOriginACL)
	}
	if strings.Contains(gotAction, "ConfirmOriginACLUpdate") {
		t.Fatalf("DescribeOriginACL used forbidden action %q", gotAction)
	}
	if gotBody != `{"ZoneId":"zone-2abcDEF123"}` {
		t.Fatalf("request body = %q", gotBody)
	}
	if info.Status != "online" || info.CurrentOriginACL == nil {
		t.Fatalf("OriginACLInfo = %#v, want online current ACL", info)
	}
}

func TestDefaultDescribeOriginACLClientHasTimeout(t *testing.T) {
	t.Parallel()

	client, ok := defaultHTTPClient.(*http.Client)
	if !ok {
		t.Fatalf("defaultHTTPClient = %T, want *http.Client", defaultHTTPClient)
	}
	if client.Timeout != DescribeOriginACLTimeout || client.Timeout <= 0 {
		t.Fatalf("default timeout = %s, want %s", client.Timeout, DescribeOriginACLTimeout)
	}
}

func TestDescribeOriginACLReportsAPIErrorWithoutBodyDump(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"Response":{"Error":{"Code":"AuthFailure","Message":"permission denied with secret should not echo"},"RequestId":"request-id"}}`))
	}))
	defer server.Close()

	client := Client{Endpoint: server.URL}
	_, err := client.DescribeOriginACL(context.Background(), Credentials{SecretID: "id", SecretKey: "key"}, "zone-2abcDEF123")
	if err == nil || !strings.Contains(err.Error(), "AuthFailure") || strings.Contains(err.Error(), "Response") {
		t.Fatalf("DescribeOriginACL() error = %v, want summarized API error without response body", err)
	}
}

func TestBuildStateValidatesOriginACLAndDomainCoverage(t *testing.T) {
	t.Parallel()

	info := &OriginACLInfo{
		Status:          "online",
		L7Hosts:         []string{"app.example.com", "*.example.net"},
		OriginACLFamily: "global",
		CurrentOriginACL: &OriginACL{
			Version:    "current-v1",
			ActiveTime: "2026-06-01T00:00:00Z",
			EntireAddresses: Addresses{
				IPv4: []string{"9.9.9.9", "8.8.8.8/24", "9.9.9.9/32"},
				IPv6: []string{"2606:4700:4700::1111"},
			},
		},
		NextOriginACL: &OriginACL{
			Version:           "next-v2",
			PlannedActiveTime: "2026-06-04T00:00:00Z",
			EntireAddresses: Addresses{
				IPv4: []string{"11.1.1.1/24"},
			},
		},
	}
	state, err := BuildState("edgeone-prod", "zone-2abcDEF123", info, []string{"app.example.com", "www.example.net"}, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("BuildState() error = %v", err)
	}
	if state.Provider != "edgeone" || state.OriginACLStatus != OriginACLStatusOnline || state.OriginACLFamily != "global" || state.CurrentVersion != "current-v1" || state.NextVersion != "next-v2" {
		t.Fatalf("state metadata = %#v", state)
	}
	if strings.Join(state.CurrentCIDRs, ",") != "8.8.8.0/24,9.9.9.9/32,2606:4700:4700::1111/128" {
		t.Fatalf("CurrentCIDRs = %#v", state.CurrentCIDRs)
	}
	if strings.Join(state.NextCIDRs, ",") != "11.1.1.0/24" {
		t.Fatalf("NextCIDRs = %#v", state.NextCIDRs)
	}
	if strings.Join(state.TrustedCIDRs, ",") != "8.8.8.0/24,9.9.9.9/32,11.1.1.0/24,2606:4700:4700::1111/128" {
		t.Fatalf("TrustedCIDRs = %#v", state.TrustedCIDRs)
	}

	updatingInfo := *info
	updatingInfo.Status = OriginACLStatusUpdating
	state, err = BuildState("edgeone-prod", "zone-2abcDEF123", &updatingInfo, []string{"app.example.com", "www.example.net"}, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("BuildState() with updating status error = %v", err)
	}
	if state.OriginACLStatus != OriginACLStatusUpdating {
		t.Fatalf("OriginACLStatus = %q, want %q", state.OriginACLStatus, OriginACLStatusUpdating)
	}
}

func TestBuildStateRejectsInvalidOriginACL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *OriginACLInfo
		want string
	}{
		{name: "nil info", info: nil, want: "OriginACLInfo is missing"},
		{name: "offline", info: &OriginACLInfo{Status: "offline"}, want: "must be online or updating"},
		{name: "missing current", info: &OriginACLInfo{Status: "online"}, want: "CurrentOriginACL is missing"},
		{name: "empty current", info: &OriginACLInfo{Status: "online", CurrentOriginACL: &OriginACL{}}, want: "CurrentOriginACL.EntireAddresses is empty"},
		{name: "missing coverage", info: &OriginACLInfo{Status: "online", L7Hosts: []string{"other.example.com"}, CurrentOriginACL: &OriginACL{EntireAddresses: Addresses{IPv4: []string{"8.8.8.8"}}}}, want: "does not cover"},
		{name: "trust all", info: &OriginACLInfo{Status: "online", L7Hosts: []string{"app.example.com"}, CurrentOriginACL: &OriginACL{EntireAddresses: Addresses{IPv4: []string{"0.0.0.0/0"}}}}, want: "trust all"},
		{name: "updating without next", info: &OriginACLInfo{Status: "updating", L7Hosts: []string{"app.example.com"}, CurrentOriginACL: &OriginACL{EntireAddresses: Addresses{IPv4: []string{"8.8.8.8"}}}}, want: "NextOriginACL is required"},
		{name: "empty next", info: &OriginACLInfo{Status: "online", L7Hosts: []string{"app.example.com"}, CurrentOriginACL: &OriginACL{EntireAddresses: Addresses{IPv4: []string{"8.8.8.8"}}}, NextOriginACL: &OriginACL{}}, want: "NextOriginACL.EntireAddresses is empty"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := BuildState("edgeone-prod", "zone-2abcDEF123", tt.info, []string{"app.example.com"}, time.Unix(1700000000, 0))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildState() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestBuildStateRejectsMissingProfileMetadata(t *testing.T) {
	t.Parallel()

	info := &OriginACLInfo{
		Status:  OriginACLStatusOnline,
		L7Hosts: []string{"app.example.com"},
		CurrentOriginACL: &OriginACL{
			EntireAddresses: Addresses{IPv4: []string{"8.8.8.8"}},
		},
	}
	tests := []struct {
		name        string
		profileName string
		zoneID      string
		want        string
	}{
		{name: "empty profile", zoneID: "zone-2abcDEF123", want: "profile name is required"},
		{name: "empty zone", profileName: "edgeone-prod", want: "zone_id is required"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := BuildState(tt.profileName, tt.zoneID, info, []string{"app.example.com"}, time.Unix(1700000000, 0))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildState() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
