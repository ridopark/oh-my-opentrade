// Package monitortest provides shared test helpers that wire a
// monitor.Service together with its indicator.Service shadow so tests
// outside the indicator package don't repeat the construction
// boilerplate. Mirrors the brokerporttest pattern.
package monitortest

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// NewSvc returns a monitor.Service wired to a fresh indicator.Service. The
// indicator.Service is also Start'd against the bus so it subscribes to
// MarketBarSanitized BEFORE callers wire monitor.Start — tests publishing
// bars to the bus need the indicator handler to fire first per the
// single-driver contract.
func NewSvc(bus ports.EventBusPort, repo ports.RepositoryPort, label string) (*monitor.Service, *indicator.Service) {
	idx := indicator.NewService(label)
	if err := idx.Start(context.Background(), bus); err != nil {
		panic("monitortest: indicator.Start: " + err.Error())
	}
	svc := monitor.NewService(bus, repo, zerolog.Nop(), monitor.WithIndicatorShadow(idx))
	return svc, idx
}
