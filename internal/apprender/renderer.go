package apprender

import (
	"bytes"
	"fmt"
	"meshify/internal/appassets"
	"meshify/internal/assets"
	"text/template"
)

type Renderer struct {
	loader appassets.Loader
}

func NewRenderer(loader appassets.Loader) Renderer {
	return Renderer{loader: loader}
}

func (renderer Renderer) Render(asset appassets.Asset, data TemplateData) ([]byte, error) {
	source, err := renderer.loader.Read(asset.SourcePath)
	if err != nil {
		return nil, err
	}
	if asset.ContentMode == assets.ContentModeCopy {
		return source, nil
	}
	tmpl, err := template.New(asset.SourcePath).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("parse app template %q: %w", asset.SourcePath, err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render app template %q: %w", asset.SourcePath, err)
	}
	return output.Bytes(), nil
}
