// Package monitortest provides shared test helpers that wire a
// monitor.Service together with its indicator.Service shadow so tests
// outside the indicator package don't repeat the construction
// boilerplate. Mirrors the brokerporttest pattern.
package monitortest

import (
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// NewSvc returns a monitor.Service wired to a fresh indicator.Service. The
// returned indicator.Service handle lets callers drive WarmUp directly when
// tests need to seed indicator state (e.g. parity flows that previously called
// monitor.WarmUpAndCollect for its side-effects on the embedded calc).
func NewSvc(bus ports.EventBusPort, repo ports.RepositoryPort, label string) (*monitor.Service, *indicator.Service) {
	idx := indicator.NewService(label)
	svc := monitor.NewService(bus, repo, zerolog.Nop(), monitor.WithIndicatorShadow(idx))
	return svc, idx
}
