package appassets

import (
	"meshify/internal/appconfig"
	"meshify/internal/assets"
	"slices"
	"strings"
	"testing"
)

func testListenConfig() appconfig.Config {
	cfg := appconfig.New()
	cfg.App.Name = "example-app"
	cfg.App.Domains = []string{"abc.com", "www.abc.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/opt/example-app/example-app --listen 127.0.0.1:18001"
	cfg.Service.WorkingDirectory = "/opt/example-app"
	return cfg
}

func TestRuntimeCatalogIncludesServiceOnlyForListenMode(t *testing.T) {
	t.Parallel()

	listenCatalog, err := RuntimeCatalog(testListenConfig())
	if err != nil {
		t.Fatalf("RuntimeCatalog() error = %v", err)
	}
	if len(listenCatalog) != 5 {
		t.Fatalf("len(listenCatalog) = %d, want 5", len(listenCatalog))
	}
	if !hasSource(listenCatalog, ServiceTemplate) {
		t.Fatalf("listen catalog missing %s", ServiceTemplate)
	}

	upstream := testListenConfig()
	upstream.App.Listen = ""
	upstream.App.Upstream = "100.64.10.20:18001"
	upstream.Service = appconfig.ServiceConfig{}
	upstreamCatalog, err := RuntimeCatalog(upstream)
	if err != nil {
		t.Fatalf("RuntimeCatalog(upstream) error = %v", err)
	}
	if len(upstreamCatalog) != 4 {
		t.Fatalf("len(upstreamCatalog) = %d, want 4", len(upstreamCatalog))
	}
	if hasSource(upstreamCatalog, ServiceTemplate) {
		t.Fatalf("upstream catalog unexpectedly includes %s", ServiceTemplate)
	}
}

func TestRuntimeCatalogMatchesCanonicalAppTemplates(t *testing.T) {
	t.Parallel()

	expected := appTemplateSourcesFromAssetCatalog(t)
	listenCatalog, err := RuntimeCatalog(testListenConfig())
	if err != nil {
		t.Fatalf("RuntimeCatalog(listen) error = %v", err)
	}
	listenSources := sourceSet(listenCatalog)
	for _, sourcePath := range expected {
		if _, ok := listenSources[sourcePath]; !ok {
			t.Fatalf("listen runtime catalog missing canonical app template %q", sourcePath)
		}
		if _, err := NewLoader().Read(sourcePath); err != nil {
			t.Fatalf("app embedded loader cannot read %q: %v", sourcePath, err)
		}
	}

	upstream := testListenConfig()
	upstream.App.Listen = ""
	upstream.App.Upstream = "100.64.10.20:18001"
	upstream.Service = appconfig.ServiceConfig{}
	upstreamCatalog, err := RuntimeCatalog(upstream)
	if err != nil {
		t.Fatalf("RuntimeCatalog(upstream) error = %v", err)
	}
	upstreamSources := sourceSet(upstreamCatalog)
	for _, sourcePath := range expected {
		_, ok := upstreamSources[sourcePath]
		if sourcePath == ServiceTemplate {
			if ok {
				t.Fatalf("upstream runtime catalog unexpectedly includes service template %q", sourcePath)
			}
			continue
		}
		if !ok {
			t.Fatalf("upstream runtime catalog missing canonical app template %q", sourcePath)
		}
	}
}

func hasSource(catalog []Asset, source string) bool {
	for _, asset := range catalog {
		if asset.SourcePath == source {
			return true
		}
	}
	return false
}

func sourceSet(catalog []Asset) map[string]struct{} {
	sources := make(map[string]struct{}, len(catalog))
	for _, asset := range catalog {
		sources[asset.SourcePath] = struct{}{}
	}
	return sources
}

func appTemplateSourcesFromAssetCatalog(t *testing.T) []string {
	t.Helper()

	sources := []string{}
	for _, asset := range assets.Catalog() {
		if !strings.HasPrefix(asset.SourcePath, "templates/app/") {
			continue
		}
		if asset.Role != assets.RoleRuntime || asset.ContentMode != assets.ContentModeRender {
			t.Fatalf("catalog asset %q role/mode = %s/%s, want runtime/render", asset.SourcePath, asset.Role, asset.ContentMode)
		}
		sources = append(sources, asset.SourcePath)
	}
	slices.Sort(sources)
	if len(sources) == 0 {
		t.Fatal("embedded asset catalog has no app runtime templates")
	}
	return sources
}
