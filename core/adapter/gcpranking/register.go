package gcpranking

import "github.com/elijahmontenegro/grudge/core"

func init() {
	core.RegisterProvider("gcpranking", func(cfg core.ProviderConfig) (any, error) {
		return New(cfg)
	})
}
