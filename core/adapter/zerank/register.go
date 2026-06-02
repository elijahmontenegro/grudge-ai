package zerank

import "github.com/elijahmontenegro/grudge/core"

func init() {
	core.RegisterProvider("zerank", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{BaseURL: cfg.BaseURL, Model: cfg.Model}), nil
	})
}
