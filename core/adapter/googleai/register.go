package googleai

import "github.com/emontenegr/grudge/core"

func init() {
	core.RegisterProvider("googleai", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{APIKey: cfg.APIKey}), nil
	})
}
