package realiprender

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lanpanel/internal/assets"
	"lanpanel/internal/realipassets"
	"text/template"
)

type Renderer struct {
	loader realipassets.Loader
}

func NewRenderer(loader realipassets.Loader) Renderer {
	return Renderer{loader: loader}
}

func (renderer Renderer) Render(asset realipassets.Asset, data TemplateData) ([]byte, error) {
	switch asset.SourcePath {
	case realipassets.StateJSONSource:
		return []byte(data.StateJSON), nil
	case realipassets.ProfileJSONSource:
		return []byte(data.ProfileJSON), nil
	case realipassets.ReferenceJSONSource:
		return []byte(data.ReferenceJSON), nil
	}
	source, err := renderer.loader.Read(asset.SourcePath)
	if err != nil {
		return nil, err
	}
	if asset.ContentMode == assets.ContentModeCopy {
		return source, nil
	}
	tmpl, err := template.New(asset.SourcePath).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("parse realip template %q: %w", asset.SourcePath, err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render realip template %q: %w", asset.SourcePath, err)
	}
	return output.Bytes(), nil
}

func marshalManagedJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal realip runtime json: %w", err)
	}
	return append(data, '\n'), nil
}
