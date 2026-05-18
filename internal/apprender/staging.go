package apprender

import (
	"fmt"
	"io/fs"
	"meshify/internal/appassets"
	"meshify/internal/appconfig"
	"meshify/internal/assets"
)

type StagedFile struct {
	SourcePath  string
	HostPath    string
	ContentMode assets.ContentMode
	Mode        fs.FileMode
	Content     []byte
}

func (renderer Renderer) Stage(catalog []appassets.Asset, data TemplateData) ([]StagedFile, error) {
	staged := make([]StagedFile, 0, len(catalog))
	for _, asset := range catalog {
		if asset.HostPath == "" {
			return nil, fmt.Errorf("app asset %q has no host path", asset.SourcePath)
		}
		content, err := renderer.Render(asset, data)
		if err != nil {
			return nil, err
		}
		staged = append(staged, StagedFile{
			SourcePath:  asset.SourcePath,
			HostPath:    asset.HostPath,
			ContentMode: asset.ContentMode,
			Mode:        asset.Mode,
			Content:     content,
		})
	}
	return staged, nil
}

func StageRuntime(cfg appconfig.Config) ([]StagedFile, error) {
	data, err := NewTemplateData(cfg)
	if err != nil {
		return nil, err
	}
	catalog, err := appassets.RuntimeCatalog(cfg)
	if err != nil {
		return nil, err
	}
	return NewRenderer(appassets.NewLoader()).Stage(catalog, data)
}
