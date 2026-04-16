package onchain

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Compile-time check that FlowTracker implements WhaleFlowPort.
var _ ports.WhaleFlowPort = (*FlowTracker)(nil)

// OnChainConfig holds configuration for the on-chain flow adapter.
type OnChainConfig struct {
	DuneAPIKey  string         `yaml:"dune_api_key"`
	DuneBaseURL string         `yaml:"dune_base_url"` // default https://api.dune.com/api/v1/
	QueryIDs    map[string]int `yaml:"query_ids"`     // asset -> Dune query ID
	CacheTTL    string         `yaml:"cache_ttl"`     // default "5m"
	Enabled     bool           `yaml:"enabled"`       // default false
}

// FlowTracker implements WhaleFlowPort by querying Dune Analytics for
// on-chain whale/custodian flow data and enriching results with wallet tags.
type FlowTracker struct {
	dune       *DuneClient
	walletTags map[string]WalletTag
	cache      *flowCache
	log        zerolog.Logger
	queryIDs   map[string]int // "BTC" -> Dune query ID
}

// NewFlowTracker creates a FlowTracker from the given config. Returns an error
// if the Dune API key is missing (when Enabled is true, this is validated by
// the caller — the constructor itself just propagates the DuneClient error).
func NewFlowTracker(cfg OnChainConfig, log zerolog.Logger) (*FlowTracker, error) {
	baseURL := cfg.DuneBaseURL
	if baseURL == "" {
		baseURL = defaultDuneBaseURL
	}

	dune, err := NewDuneClient(cfg.DuneAPIKey, baseURL, log)
	if err != nil {
		return nil, fmt.Errorf("onchain: create dune client: %w", err)
	}

	cacheTTL := defaultCacheTTL
	if cfg.CacheTTL != "" {
		parsed, err := time.ParseDuration(cfg.CacheTTL)
		if err != nil {
			return nil, fmt.Errorf("onchain: parse cache_ttl %q: %w", cfg.CacheTTL, err)
		}
		cacheTTL = parsed
	}

	return &FlowTracker{
		dune:       dune,
		walletTags: DefaultWalletTags(),
		cache:      newFlowCache(cacheTTL),
		log:        log.With().Str("component", "flow_tracker").Logger(),
		queryIDs:   cfg.QueryIDs,
	}, nil
}

// newFlowTrackerForTest creates a FlowTracker with an injected DuneClient (for testing).
func newFlowTrackerForTest(dune *DuneClient, walletTags map[string]WalletTag, queryIDs map[string]int, cacheTTL time.Duration, log zerolog.Logger) *FlowTracker {
	return &FlowTracker{
		dune:       dune,
		walletTags: walletTags,
		cache:      newFlowCache(cacheTTL),
		log:        log.With().Str("component", "flow_tracker").Logger(),
		queryIDs:   queryIDs,
	}
}

// NetFlow returns the net exchange flow for an asset over the given window.
// Positive = net inflow to exchanges (selling pressure), negative = outflow (accumulation).
func (ft *FlowTracker) NetFlow(ctx context.Context, asset string, windowHrs int) (ports.NetFlowResult, error) {
	cacheKey := fmt.Sprintf("netflow:%s:%d", asset, windowHrs)
	if cached, ok := ft.cache.Get(cacheKey); ok {
		ft.log.Debug().Str("asset", asset).Int("window_hrs", windowHrs).Msg("cache hit for net flow")
		return cached.(ports.NetFlowResult), nil
	}

	queryID, ok := ft.queryIDs[asset]
	if !ok {
		return ports.NetFlowResult{}, fmt.Errorf("onchain: no query ID configured for asset %q", asset)
	}

	params := map[string]string{
		"window_hours": strconv.Itoa(windowHrs),
	}

	rows, err := ft.executeAndGetResults(ctx, queryID, params)
	if err != nil {
		return ports.NetFlowResult{}, fmt.Errorf("onchain: net flow query for %s: %w", asset, err)
	}

	result := ft.computeNetFlow(asset, windowHrs, rows)

	ft.cache.Set(cacheKey, result)
	return result, nil
}

// LargeTransfers returns individual large transfers above minUSD in the window.
func (ft *FlowTracker) LargeTransfers(ctx context.Context, asset string, windowHrs int, minUSD float64) ([]ports.Transfer, error) {
	cacheKey := fmt.Sprintf("large:%s:%d:%.0f", asset, windowHrs, minUSD)
	if cached, ok := ft.cache.Get(cacheKey); ok {
		ft.log.Debug().Str("asset", asset).Float64("min_usd", minUSD).Msg("cache hit for large transfers")
		return cached.([]ports.Transfer), nil
	}

	queryID, ok := ft.queryIDs[asset]
	if !ok {
		return nil, fmt.Errorf("onchain: no query ID configured for asset %q", asset)
	}

	params := map[string]string{
		"window_hours": strconv.Itoa(windowHrs),
		"min_usd":      strconv.FormatFloat(minUSD, 'f', 0, 64),
	}

	rows, err := ft.executeAndGetResults(ctx, queryID, params)
	if err != nil {
		return nil, fmt.Errorf("onchain: large transfers query for %s: %w", asset, err)
	}

	transfers := ft.parseTransfers(asset, rows, minUSD)

	ft.cache.Set(cacheKey, transfers)
	return transfers, nil
}

// executeAndGetResults runs a Dune query and waits for results.
func (ft *FlowTracker) executeAndGetResults(ctx context.Context, queryID int, params map[string]string) ([]map[string]any, error) {
	// Try cached latest results first to save credits.
	rows, err := ft.dune.GetLatestResults(ctx, queryID)
	if err == nil && len(rows) > 0 {
		ft.log.Debug().Int("query_id", queryID).Int("rows", len(rows)).Msg("using cached Dune results")
		return rows, nil
	}

	// Fall back to fresh execution.
	execID, err := ft.dune.ExecuteQuery(ctx, queryID, params)
	if err != nil {
		return nil, err
	}

	return ft.dune.GetResults(ctx, execID)
}

// computeNetFlow aggregates raw transfer rows into a NetFlowResult.
// Expected row schema: from_address, to_address, amount_usd, amount, tx_hash, block_time.
func (ft *FlowTracker) computeNetFlow(asset string, windowHrs int, rows []map[string]any) ports.NetFlowResult {
	var inFlow, outFlow float64
	var largeCount int

	for _, row := range rows {
		amountUSD := parseFloat64(row["amount_usd"])
		fromAddr := parseString(row["from_address"])
		toAddr := parseString(row["to_address"])

		_, fromIsExchange := ft.walletTags[fromAddr]
		fromExchange := fromIsExchange && ft.walletTags[fromAddr].IsExchange()
		_, toIsExchange := ft.walletTags[toAddr]
		toExchange := toIsExchange && ft.walletTags[toAddr].IsExchange()

		if toExchange && !fromExchange {
			// Inflow to exchange (selling pressure).
			inFlow += amountUSD
		} else if fromExchange && !toExchange {
			// Outflow from exchange (accumulation).
			outFlow += amountUSD
		}

		if amountUSD >= 1_000_000 {
			largeCount++
		}
	}

	return ports.NetFlowResult{
		Asset:      asset,
		WindowHrs:  windowHrs,
		NetFlowUSD: inFlow - outFlow,
		InFlowUSD:  inFlow,
		OutFlowUSD: outFlow,
		LargeCount: largeCount,
		Timestamp:  time.Now(),
	}
}

// parseTransfers converts raw Dune rows to Transfer structs, filtering by minUSD.
func (ft *FlowTracker) parseTransfers(asset string, rows []map[string]any, minUSD float64) []ports.Transfer {
	var transfers []ports.Transfer

	for _, row := range rows {
		amountUSD := parseFloat64(row["amount_usd"])
		if amountUSD < minUSD {
			continue
		}

		fromAddr := parseString(row["from_address"])
		toAddr := parseString(row["to_address"])

		var fromTag, toTag string
		var venue domain.Venue

		if wt, ok := ft.walletTags[fromAddr]; ok {
			fromTag = wt.Tag
			if wt.IsExchange() {
				venue = tagToVenue(wt.Tag)
			}
		}
		if wt, ok := ft.walletTags[toAddr]; ok {
			toTag = wt.Tag
			if wt.IsExchange() {
				venue = tagToVenue(wt.Tag)
			}
		}

		var ts time.Time
		if raw, ok := row["block_time"]; ok {
			if s, ok := raw.(string); ok {
				ts, _ = time.Parse(time.RFC3339, s)
			}
		}

		transfers = append(transfers, ports.Transfer{
			TxHash:    parseString(row["tx_hash"]),
			Asset:     asset,
			From:      fromAddr,
			FromTag:   fromTag,
			To:        toAddr,
			ToTag:     toTag,
			AmountUSD: amountUSD,
			Amount:    parseFloat64(row["amount"]),
			Timestamp: ts,
			Venue:     venue,
		})
	}

	return transfers
}

// tagToVenue maps a wallet tag prefix to a domain.Venue. Returns VenueUnspecified
// if the exchange cannot be determined.
func tagToVenue(tag string) domain.Venue {
	switch {
	case len(tag) >= 7 && tag[:7] == "binance":
		return domain.VenueBinance
	case len(tag) >= 8 && tag[:8] == "coinbase":
		return domain.VenueCoinbase
	case len(tag) >= 6 && tag[:6] == "kraken":
		return domain.VenueKraken
	default:
		return domain.VenueUnspecified
	}
}
