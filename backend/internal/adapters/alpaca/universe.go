package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

var allowedExchanges = map[string]bool{
	"NASDAQ": true,
	"NYSE":   true,
	"ARCA":   true,
	"BATS":   true,
}

func isScreenableEquity(exchange, symbol, name string, shortable, marginable, fractionable bool) bool {
	if !allowedExchanges[exchange] {
		return false
	}
	if !shortable && !marginable {
		return false
	}
	if !fractionable {
		return false
	}
	nameUpper := strings.ToUpper(name)
	for _, pattern := range junkNamePatterns {
		if strings.Contains(nameUpper, pattern) {
			return false
		}
	}
	if strings.Contains(symbol, " ") || strings.Contains(symbol, "-") {
		return false
	}
	return true
}

var junkNamePatterns = []string{
	"WARRANT",
	" RIGHT",
	" UNIT",
	"ACQUISITION CORP",
	"BLANK CHECK",
	"DEPOSITARY",
	"PREFERRED",
	" ETF",
}

type cachedAssets struct {
	assets    []ports.Asset
	fetchedAt time.Time
}

const universeCacheTTL = 24 * time.Hour

var (
	universeCache   sync.Map
	universeCacheMu sync.Mutex
)

func (c *RESTClient) ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error) {
	cacheKey := string(assetClass)
	if cached, ok := universeCache.Load(cacheKey); ok {
		entry := cached.(*cachedAssets)
		if time.Since(entry.fetchedAt) < universeCacheTTL {
			return entry.assets, nil
		}
	}

	universeCacheMu.Lock()
	defer universeCacheMu.Unlock()

	if cached, ok := universeCache.Load(cacheKey); ok {
		entry := cached.(*cachedAssets)
		if time.Since(entry.fetchedAt) < universeCacheTTL {
			return entry.assets, nil
		}
	}

	alpacaClass := "us_equity"
	if assetClass == domain.AssetClassCrypto {
		alpacaClass = "crypto"
	}

	path := fmt.Sprintf("/v2/assets?status=active&asset_class=%s", alpacaClass)
	resp, err := c.doReqWithOpts(ctx, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 2})
	if err != nil {
		c.log.Error().Err(err).Str("asset_class", alpacaClass).Msg("list tradeable assets HTTP request failed")
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alpaca: list tradeable assets failed (status %d): %s", resp.StatusCode, string(body))
	}

	var raw []struct {
		ID           string `json:"id"`
		Class        string `json:"class"`
		Exchange     string `json:"exchange"`
		Symbol       string `json:"symbol"`
		Name         string `json:"name"`
		Status       string `json:"status"`
		Tradable     bool   `json:"tradable"`
		Shortable    bool   `json:"shortable"`
		Marginable   bool   `json:"marginable"`
		Fractionable bool   `json:"fractionable"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("alpaca: decode tradeable assets: %w", err)
	}

	var assets []ports.Asset
	skipped := 0
	for _, a := range raw {
		if !a.Tradable {
			continue
		}
		ac := domain.AssetClassEquity
		if a.Class == "crypto" {
			ac = domain.AssetClassCrypto
		}

		// For equities, filter out junk to reduce universe from ~12K to ~3K.
		if ac == domain.AssetClassEquity && !isScreenableEquity(a.Exchange, a.Symbol, a.Name, a.Shortable, a.Marginable, a.Fractionable) {
			skipped++
			continue
		}

		// For crypto, only keep /USD pairs — Alpaca lists /USDT, /USDC, etc.
		// as tradeable but doesn't serve market data (bars) for them.
		if ac == domain.AssetClassCrypto && !strings.HasSuffix(a.Symbol, "/USD") {
			skipped++
			continue
		}

		assets = append(assets, ports.Asset{
			Symbol:     a.Symbol,
			Name:       a.Name,
			AssetClass: ac,
			Exchange:   a.Exchange,
			Tradeable:  true,
		})
	}

	universeCache.Store(cacheKey, &cachedAssets{
		assets:    assets,
		fetchedAt: time.Now(),
	})

	c.log.Info().
		Str("asset_class", alpacaClass).
		Int("total_raw", len(raw)).
		Int("kept", len(assets)).
		Int("filtered_out", skipped).
		Msg("tradeable assets fetched and cached")

	return assets, nil
}
