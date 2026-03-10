package execution

import (
	"context"
	"fmt"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type Cluster string

const (
	ClusterTechEquity Cluster = "tech_equity"
	ClusterDefensive  Cluster = "defensive"
	ClusterCrypto     Cluster = "crypto"
)

type ExposureGuard struct {
	broker ports.BrokerPort
	caps   map[Cluster]float64
	log    zerolog.Logger
}

func NewExposureGuard(broker ports.BrokerPort, accountEquity float64, log zerolog.Logger) *ExposureGuard {
	return &ExposureGuard{
		broker: broker,
		caps: map[Cluster]float64{
			ClusterTechEquity: accountEquity * 0.35,
			ClusterDefensive:  accountEquity * 0.30,
			ClusterCrypto:     accountEquity * 0.35,
		},
		log: log,
	}
}

func (g *ExposureGuard) UpdateCaps(accountEquity float64) {
	g.caps[ClusterTechEquity] = accountEquity * 0.35
	g.caps[ClusterDefensive] = accountEquity * 0.30
	g.caps[ClusterCrypto] = accountEquity * 0.35
}

func (g *ExposureGuard) Check(ctx context.Context, intent domain.OrderIntent) error {
	if intent.Direction.IsExit() {
		return nil
	}

	positions, err := g.broker.GetPositions(ctx, intent.TenantID, intent.EnvMode)
	if err != nil {
		g.log.Error().Err(err).Msg("exposure guard: failed to fetch positions — allowing order through")
		return nil
	}

	exposure := make(map[Cluster]float64)
	for _, p := range positions {
		cluster := classifySymbol(p.Symbol)
		exposure[cluster] += p.Quantity * p.Price
	}

	intentCluster := classifySymbol(intent.Symbol)
	intentNotional := intent.Quantity * intent.LimitPrice
	projectedExposure := exposure[intentCluster] + intentNotional
	cap := g.caps[intentCluster]

	if projectedExposure > cap {
		return fmt.Errorf("exposure_guard: %s cluster would reach $%.0f, exceeding cap $%.0f",
			intentCluster, projectedExposure, cap)
	}

	g.log.Debug().
		Str("cluster", string(intentCluster)).
		Float64("current", exposure[intentCluster]).
		Float64("projected", projectedExposure).
		Float64("cap", cap).
		Msg("exposure guard passed")
	return nil
}

var defensiveSymbols = map[domain.Symbol]struct{}{
	"SPY": {}, "QQQ": {}, "IWM": {}, "DIA": {}, "VOO": {},
	"VTI": {}, "TLT": {}, "GLD": {}, "SLV": {}, "XLU": {},
}

func classifySymbol(sym domain.Symbol) Cluster {
	if strings.Contains(string(sym), "/") {
		return ClusterCrypto
	}
	if _, ok := defensiveSymbols[sym]; ok {
		return ClusterDefensive
	}
	return ClusterTechEquity
}
