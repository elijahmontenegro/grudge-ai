package tei

import "github.com/emontenegr/spidey/core"

func init() {
	core.RegisterProvider("tei", func(cfg core.ProviderConfig) (core.Provider, error) {
		return New(Config{BaseURL: cfg.BaseURL}), nil
	})
}
