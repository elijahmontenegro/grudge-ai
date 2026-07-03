package vertex

import "github.com/elijahmontenegro/grudge/core"

func init() {
	core.RegisterProvider("vertex", func(cfg core.ProviderConfig) (any, error) {
		return New(cfg)
	})
}
