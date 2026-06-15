package realipassets

import (
	"fmt"
	"io/fs"
	"lanpanel/internal/assets"
	"lanpanel/internal/components/appsvc"
	"lanpanel/internal/realip"
	"slices"
)

type Asset struct {
	SourcePath  string
	ContentMode assets.ContentMode
	HostPath    string
	Mode        fs.FileMode
}

const (
	NginxRealIPTemplate      = "templates/realip/nginx-realip.conf.tmpl"
	TrustedCIDRsTemplate     = "templates/realip/trusted-cidrs.conf.tmpl"
	RefreshServiceTemplate   = "templates/realip/refresh.service.tmpl"
	RefreshTimerTemplate     = "templates/realip/refresh.timer.tmpl"
	StateJSONSource          = "generated/realip/state.json"
	ProfileJSONSource        = "generated/realip/profile.json"
	ReferenceJSONSource      = "generated/realip/reference.json"
	DefaultRefreshBinaryPath = "/usr/local/bin/lanpanel"
)

func RuntimeCatalog(profile realip.ProfileConfig, appName string) ([]Asset, error) {
	names, err := appsvc.NewRealIPProfileNames(profile.Name, profile.Provider, appName)
	if err != nil {
		return nil, err
	}
	items := []Asset{
		{SourcePath: NginxRealIPTemplate, ContentMode: assets.ContentModeRender, HostPath: names.NginxIncludePath, Mode: 0o644},
		{SourcePath: TrustedCIDRsTemplate, ContentMode: assets.ContentModeRender, HostPath: names.TrustedCIDRPath, Mode: 0o644},
		{SourcePath: RefreshServiceTemplate, ContentMode: assets.ContentModeRender, HostPath: names.RefreshServicePath, Mode: 0o644},
		{SourcePath: RefreshTimerTemplate, ContentMode: assets.ContentModeRender, HostPath: names.RefreshTimerPath, Mode: 0o644},
		{SourcePath: StateJSONSource, ContentMode: assets.ContentModeRender, HostPath: names.StatePath, Mode: 0o600},
		{SourcePath: ProfileJSONSource, ContentMode: assets.ContentModeRender, HostPath: names.MetadataPath, Mode: 0o600},
	}
	if names.ReferencePathForApp != "" {
		items = append(items, Asset{SourcePath: ReferenceJSONSource, ContentMode: assets.ContentModeRender, HostPath: names.ReferencePathForApp, Mode: 0o600})
	}
	return cloneCatalog(items), nil
}

func MustRuntimeCatalog(profile realip.ProfileConfig, appName string) []Asset {
	items, err := RuntimeCatalog(profile, appName)
	if err != nil {
		panic(fmt.Sprintf("realipassets: runtime catalog: %v", err))
	}
	return items
}

func cloneCatalog(items []Asset) []Asset {
	cloned := append([]Asset(nil), items...)
	slices.SortFunc(cloned, func(left Asset, right Asset) int {
		switch {
		case left.SourcePath < right.SourcePath:
			return -1
		case left.SourcePath > right.SourcePath:
			return 1
		default:
			return 0
		}
	})
	return cloned
}
