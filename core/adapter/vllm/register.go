package vllm

import "github.com/emontenegr/spidey/core"

func init() {
	core.RegisterProvider("vllm", func(cfg core.ProviderConfig) (core.Provider, error) {
		return New(Config{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey}), nil
	})
}
