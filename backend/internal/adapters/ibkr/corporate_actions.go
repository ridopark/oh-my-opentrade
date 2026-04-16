// IBKR corporate-actions client stub (TODO-sprint-6.3). IBKR exposes
// corporate actions through a separate subscription ("US Securities
// Snapshot and Futures Value Bundle" / reqFundamentalData 'CalendarReport')
// that we have not yet wired. This file reserves the type and import path
// so application code can depend on the interface; every method returns
// ports.ErrCorporateActionsNotImplemented.
package ibkr

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// CorporateActionsClient is a placeholder for the IBKR corporate-actions
// feed. It is zero-value safe; a nil receiver returns the same
// not-implemented sentinel as a populated one.
type CorporateActionsClient struct {
	log zerolog.Logger
}

// NewCorporateActionsClient returns a stub client. It never fails.
func NewCorporateActionsClient(log zerolog.Logger) *CorporateActionsClient {
	return &CorporateActionsClient{
		log: log.With().Str("component", "ibkr_corporate_actions_stub").Logger(),
	}
}

// GetSplits is a stub and always returns ErrCorporateActionsNotImplemented.
func (c *CorporateActionsClient) GetSplits(_ context.Context, _ string, _, _ time.Time) ([]ports.CorporateAction, error) {
	return nil, ports.ErrCorporateActionsNotImplemented
}
