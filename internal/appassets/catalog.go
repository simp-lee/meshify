package appassets

import (
	"fmt"
	"io/fs"
	"lanpanel/internal/appconfig"
	"lanpanel/internal/assets"
	"lanpanel/internal/components/appsvc"
	"slices"
)

type Asset struct {
	SourcePath  string
	ContentMode assets.ContentMode
	HostPath    string
	Mode        fs.FileMode
}

const (
	NginxTemplate             = "templates/app/nginx.conf.tmpl"
	ServiceTemplate           = "templates/app/service.tmpl"
	RenewServiceTemplate      = "templates/app/lego-renew.service.tmpl"
	RenewTimerTemplate        = "templates/app/lego-renew.timer.tmpl"
	HookTemplate              = "templates/app/install-cert-and-reload-nginx.sh.tmpl"
	GoAccessConfigTemplate    = "templates/app/goaccess.conf.tmpl"
	GoAccessServiceTemplate   = "templates/app/goaccess.service.tmpl"
	GoAccessLogrotateTemplate = "templates/app/goaccess-logrotate.tmpl"
)

func RuntimeCatalog(cfg appconfig.Config) ([]Asset, error) {
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		return nil, err
	}
	items := []Asset{
		{SourcePath: NginxTemplate, ContentMode: assets.ContentModeRender, HostPath: names.NginxAvailablePath, Mode: 0o644},
		{SourcePath: RenewServiceTemplate, ContentMode: assets.ContentModeRender, HostPath: "/etc/systemd/system/" + names.RenewServiceUnit, Mode: 0o644},
		{SourcePath: RenewTimerTemplate, ContentMode: assets.ContentModeRender, HostPath: "/etc/systemd/system/" + names.RenewTimerUnit, Mode: 0o644},
		{SourcePath: HookTemplate, ContentMode: assets.ContentModeRender, HostPath: names.HookPath, Mode: 0o755},
	}
	if cfg.Mode() == appconfig.ModeListen {
		items = append(items, Asset{SourcePath: ServiceTemplate, ContentMode: assets.ContentModeRender, HostPath: "/etc/systemd/system/" + names.ServiceUnit, Mode: 0o644})
	}
	if cfg.Nginx.GoAccess.Enabled {
		items = append(items,
			Asset{SourcePath: GoAccessConfigTemplate, ContentMode: assets.ContentModeRender, HostPath: names.GoAccessConfigPath, Mode: 0o644},
			Asset{SourcePath: GoAccessServiceTemplate, ContentMode: assets.ContentModeRender, HostPath: "/etc/systemd/system/" + names.GoAccessServiceUnit, Mode: 0o644},
		)
		if appsvc.GoAccessManagesCanonicalAccessLog(cfg) {
			items = append(items, Asset{SourcePath: GoAccessLogrotateTemplate, ContentMode: assets.ContentModeRender, HostPath: names.GoAccessLogrotatePath, Mode: 0o644})
		}
	}
	return cloneCatalog(items), nil
}

func MustRuntimeCatalog(cfg appconfig.Config) []Asset {
	items, err := RuntimeCatalog(cfg)
	if err != nil {
		panic(fmt.Sprintf("appassets: runtime catalog: %v", err))
	}
	return items
}

func cloneCatalog(items []Asset) []Asset {
	cloned := make([]Asset, 0, len(items))
	cloned = append(cloned, items...)
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
