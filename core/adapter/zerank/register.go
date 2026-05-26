package zerank

import "github.com/emontenegr/spidey/core"

func init() {
	core.RegisterProvider("zerank", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{BaseURL: cfg.BaseURL, Model: cfg.Model}), nil
	})
}
