package realipassets

import (
	"lanpanel/internal/realip"
	"testing"
)

func TestRuntimeCatalogDerivesProfileArtifacts(t *testing.T) {
	t.Parallel()

	catalog, err := RuntimeCatalog(realip.ProfileConfig{Name: "edgeone-prod", Provider: "edgeone"}, "example-app")
	if err != nil {
		t.Fatalf("RuntimeCatalog() error = %v", err)
	}
	if len(catalog) != 7 {
		t.Fatalf("len(catalog) = %d, want 7", len(catalog))
	}
	bySource := map[string]Asset{}
	for _, asset := range catalog {
		bySource[asset.SourcePath] = asset
	}
	tests := map[string]string{
		NginxRealIPTemplate:  "/etc/nginx/lanpanel/realip/edgeone-prod/active.conf",
		TrustedCIDRsTemplate: "/etc/nginx/lanpanel/realip/edgeone-prod/trusted-cidrs.conf",
		RefreshServiceTemplate: "/etc/systemd/system/" +
			"lanpanel-realip-edgeone-prod-refresh.service",
		RefreshTimerTemplate: "/etc/systemd/system/" +
			"lanpanel-realip-edgeone-prod-refresh.timer",
		StateJSONSource:     "/var/lib/lanpanel/realip/edgeone-prod/state.json",
		ProfileJSONSource:   "/var/lib/lanpanel/realip/edgeone-prod/profile.json",
		ReferenceJSONSource: "/var/lib/lanpanel/realip/edgeone-prod/references/example-app.json",
	}
	for source, hostPath := range tests {
		asset, ok := bySource[source]
		if !ok {
			t.Fatalf("catalog missing %s", source)
		}
		if asset.HostPath != hostPath {
			t.Fatalf("%s HostPath = %q, want %q", source, asset.HostPath, hostPath)
		}
	}
}
