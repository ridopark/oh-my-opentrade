package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type newsResponse struct {
	News []newsArticle `json:"news"`
}

type newsArticle struct {
	ID        int64    `json:"id"`
	Headline  string   `json:"headline"`
	Summary   string   `json:"summary"`
	Source    string   `json:"source"`
	Symbols   []string `json:"symbols"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	URL       string   `json:"url"`
}

type cachedNews struct {
	items     []domain.NewsItem
	fetchedAt time.Time
}

type NewsClient struct {
	dataURL   string
	apiKey    string
	apiSecret string
	client    *http.Client
	cache     sync.Map
	cacheTTL  time.Duration
	logger    *slog.Logger
}

func NewNewsClient(dataURL, apiKey, apiSecret string, logger *slog.Logger) *NewsClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &NewsClient{
		dataURL:   strings.TrimSuffix(dataURL, "/"),
		apiKey:    apiKey,
		apiSecret: apiSecret,
		client:    &http.Client{Timeout: 10 * time.Second},
		cacheTTL:  5 * time.Minute,
		logger:    logger.With("component", "alpaca_news"),
	}
}

func normalizeSymbolForNews(symbol string) string {
	return strings.ReplaceAll(symbol, "/", "")
}

func (nc *NewsClient) GetRecentNews(ctx context.Context, symbol string, since time.Duration) ([]domain.NewsItem, error) {
	cacheKey := symbol

	if entry, ok := nc.cache.Load(cacheKey); ok {
		cached := entry.(cachedNews)
		if time.Since(cached.fetchedAt) < nc.cacheTTL {
			return cached.items, nil
		}
	}

	alpacaSymbol := normalizeSymbolForNews(symbol)
	start := time.Now().UTC().Add(-since).Format(time.RFC3339)

	params := url.Values{}
	params.Set("symbols", alpacaSymbol)
	params.Set("start", start)
	params.Set("limit", "5")
	params.Set("sort", "desc")

	reqURL := fmt.Sprintf("%s/v1beta1/news?%s", nc.dataURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		nc.logger.Warn("failed to create news request", "symbol", symbol, "error", err)
		return nil, nil
	}
	req.Header.Set(headerAPIKey, nc.apiKey)
	req.Header.Set(headerAPISecret, nc.apiSecret)

	resp, err := nc.client.Do(req)
	if err != nil {
		nc.logger.Warn("news API request failed", "symbol", symbol, "error", err)
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		nc.logger.Warn("news API returned non-2xx", "symbol", symbol, "status", resp.StatusCode)
		return nil, nil
	}

	var result newsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		nc.logger.Warn("failed to parse news response", "symbol", symbol, "error", err)
		return nil, nil
	}

	items := make([]domain.NewsItem, 0, len(result.News))
	for _, a := range result.News {
		createdAt, _ := time.Parse(time.RFC3339, a.CreatedAt)
		updatedAt, _ := time.Parse(time.RFC3339, a.UpdatedAt)
		items = append(items, domain.NewsItem{
			ID:        fmt.Sprintf("%d", a.ID),
			Headline:  a.Headline,
			Summary:   a.Summary,
			Source:    a.Source,
			Symbols:   a.Symbols,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			URL:       a.URL,
		})
	}

	nc.cache.Store(cacheKey, cachedNews{items: items, fetchedAt: time.Now()})
	return items, nil
}
