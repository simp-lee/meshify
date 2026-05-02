package render

import (
	"fmt"
	"io/fs"
	"meshify/internal/assets"
	"meshify/internal/config"
	"slices"
)

type StagedFile struct {
	SourcePath  string
	HostPath    string
	ContentMode assets.ContentMode
	Mode        fs.FileMode
	Activations []assets.Activation
	Content     []byte
}

func (renderer Renderer) Stage(catalog []assets.Asset, data TemplateData) ([]StagedFile, error) {
	staged := make([]StagedFile, 0, len(catalog))
	for _, asset := range catalog {
		if asset.HostPath == "" {
			return nil, fmt.Errorf("asset %q has no host path", asset.SourcePath)
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
			Activations: slices.Clone(asset.Activations),
			Content:     content,
		})
	}
	return staged, nil
}

func StageRuntime(cfg config.Config) ([]StagedFile, error) {
	data, err := NewTemplateData(cfg)
	if err != nil {
		return nil, err
	}

	renderer := NewRenderer(assets.NewLoader())
	return renderer.Stage(assets.RuntimeCatalog(), data)
}
