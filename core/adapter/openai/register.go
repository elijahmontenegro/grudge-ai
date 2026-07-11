package openai

import "github.com/elijahmontenegro/grudge/core"

func init() {
	core.RegisterProvider("openai", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}), nil
	})
}
