package appassets

import "meshify/internal/assets"

type Loader struct {
	loader assets.Loader
}

func NewLoader() Loader {
	return Loader{loader: assets.NewLoader()}
}

func (loader Loader) Read(sourcePath string) ([]byte, error) {
	return loader.loader.Read(sourcePath)
}
