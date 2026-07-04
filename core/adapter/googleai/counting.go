package googleai

import (
	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/genaikit"
)

func init() { core.RegisterCountProjection("googleai", genaikit.CountText) }
