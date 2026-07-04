package googleai

import "github.com/elijahmontenegro/grudge/core"

func init() {
	core.RegisterProvider("googleai", func(cfg core.ProviderConfig) (any, error) {
		return New(cfg)
	})
}
