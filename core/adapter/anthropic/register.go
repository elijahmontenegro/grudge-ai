package anthropic

import "github.com/emontenegr/spidey/core"

func init() {
	core.RegisterProvider("anthropic", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}), nil
	})
	core.RegisterCodec(Codec{})
}
