package appconfig

import (
	"fmt"
	"meshify/internal/assets"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const exampleAssetPath = "config/meshify-app.yaml.example"

func (c Config) ExportYAML() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal app config yaml: %w", err)
	}
	return data, nil
}

func (c Config) WriteFile(path string) error {
	data, err := c.ExportYAML()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create app config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write app config file: %w", err)
	}
	return nil
}

func ExampleYAML() ([]byte, error) {
	data, err := assets.NewLoader().Read(exampleAssetPath)
	if err != nil {
		return nil, err
	}
	if _, err := LoadBytes(data); err != nil {
		return nil, fmt.Errorf("embedded app example config is invalid: %w", err)
	}
	return data, nil
}

func WriteExampleFile(path string) error {
	data, err := ExampleYAML()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create app example config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write app example config file: %w", err)
	}
	return nil
}
