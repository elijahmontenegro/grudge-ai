package ollama

import "github.com/elijahmontenegro/grudge/core"

func init() {
	core.RegisterProvider("ollama", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{BaseURL: cfg.BaseURL}), nil
	})
}
