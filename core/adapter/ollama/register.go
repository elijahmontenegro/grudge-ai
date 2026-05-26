package ollama

import "github.com/emontenegr/spidey/core"

func init() {
	core.RegisterProvider("ollama", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{BaseURL: cfg.BaseURL}), nil
	})
}
