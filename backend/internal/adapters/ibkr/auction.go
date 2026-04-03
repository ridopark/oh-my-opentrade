package ibkr

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/scmhub/ibsync"
)

// StreamAuctionData subscribes to NYSE auction imbalance data for the given symbols
// via IBKR tick type 225. It polls every 5 seconds between 15:40-16:00 ET and publishes
// EventAuctionImbalance events. Only meaningful for live trading (not backtests).
func (a *Adapter) StreamAuctionData(ctx context.Context, symbols []domain.Symbol, bus ports.EventBusPort, tenantID string, envMode domain.EnvMode) error {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}

	ib := a.conn.IB()

	// Create contracts and subscribe
	type auctionSub struct {
		symbol   domain.Symbol
		contract *ibsync.Contract
		ticker   *ibsync.Ticker
	}
	subs := make([]auctionSub, 0, len(symbols))

	for _, sym := range symbols {
		contract := &ibsync.Contract{
			Symbol:   sym.String(),
			SecType:  "STK",
			Exchange: "SMART",
			Currency: "USD",
		}
		ticker := ib.ReqMktData(contract, "225")
		subs = append(subs, auctionSub{symbol: sym, contract: contract, ticker: ticker})
		a.log.Info().Str("symbol", sym.String()).Msg("subscribed to auction imbalance data")
	}

	defer func() {
		for _, sub := range subs {
			ib.CancelMktData(sub.contract)
		}
		a.log.Info().Int("symbols", len(subs)).Msg("canceled all auction imbalance subscriptions")
	}()

	// Poll every 5 seconds during auction window
	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
			now := time.Now().In(loc)
			hour, min := now.Hour(), now.Minute()

			// Only active 15:40-16:00 ET
			if hour != 15 || min < 40 {
				continue
			}

			for _, sub := range subs {
				imbalance := sub.ticker.AuctionImbalance().Float()
				volume := sub.ticker.AuctionVolume().Float()
				price := sub.ticker.AuctionPrice()

				if imbalance == 0 && volume == 0 {
					continue // no data yet
				}

				snap := domain.AuctionImbalanceSnapshot{
					Time:      now,
					Symbol:    sub.symbol,
					Volume:    volume,
					Price:     price,
					Imbalance: imbalance,
				}
				evt, evtErr := domain.NewEvent(
					domain.EventAuctionImbalance,
					tenantID,
					envMode,
					now.Format(time.RFC3339Nano)+"-auction-"+sub.symbol.String(),
					snap,
				)
				if evtErr != nil {
					a.log.Error().Err(evtErr).Str("symbol", sub.symbol.String()).Msg("failed to create auction event")
					continue
				}
				if pubErr := bus.Publish(ctx, *evt); pubErr != nil {
					a.log.Error().Err(pubErr).Str("symbol", sub.symbol.String()).Msg("failed to publish auction event")
				}
			}
		}
	}
}
