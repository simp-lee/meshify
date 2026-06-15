package realipassets

import "lanpanel/internal/assets"

type Loader interface {
	Read(path string) ([]byte, error)
}

func NewLoader() Loader {
	return assets.NewLoader()
}
