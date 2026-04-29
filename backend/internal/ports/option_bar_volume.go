package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// OptionBarVolumePort returns the contract count traded for an option OCC
// during the bar containing ts on the given timeframe. Used by SimBroker
// market-impact modeling to compute participation = qty*100 / bar_volume.
//
// Implementations must return (0, nil) for bars with no trades — the caller
// treats both (0, nil) and (_, err) as "data unavailable, no impact applied".
type OptionBarVolumePort interface {
	BarVolume(ctx context.Context, occ domain.Symbol, ts time.Time, tf domain.Timeframe) (int64, error)
}
