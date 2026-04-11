package builtin

import (
	"sync"
	"time"
)

// tzCache memoizes time.LoadLocation results. Strategy gate checks
// (AllowedHoursStart/End) call this on every bar for every symbol, so the
// naive LoadLocation path showed up as ~5% of backtest CPU in pprof.
var tzCache sync.Map // map[string]*time.Location

// cachedLocation returns the parsed IANA location for name, loading and
// caching it on first use. On load failure it returns nil, matching the
// previous behavior of callers that checked err before using the result.
func cachedLocation(name string) *time.Location {
	if name == "" {
		return nil
	}
	if v, ok := tzCache.Load(name); ok {
		return v.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	tzCache.Store(name, loc)
	return loc
}
