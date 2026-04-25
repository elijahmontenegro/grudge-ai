package openai

import "github.com/emontenegr/spidey/core"

func init() {
	core.RegisterProvider("openai", func(cfg core.ProviderConfig) (core.Provider, error) {
		return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}), nil
	})
}
