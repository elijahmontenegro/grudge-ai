package googleai

import "github.com/emontenegr/spidey/core"

func init() {
	core.RegisterProvider("googleai", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{APIKey: cfg.APIKey}), nil
	})
}
