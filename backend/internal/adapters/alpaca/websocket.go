package alpaca

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// WSClient handles WebSocket connections for Alpaca market data.
type WSClient struct {
	dataURL   string
	apiKey    string
	apiSecret string
	closeOnce sync.Once
}

// NewWSClient creates a new WSClient instance.
func NewWSClient(dataURL string, apiKey string, apiSecret string) *WSClient {
	return &WSClient{
		dataURL:   dataURL,
		apiKey:    apiKey,
		apiSecret: apiSecret,
	}
}

type alpacaBar struct {
	T    string  `json:"T"`
	S    string  `json:"S"`
	O    float64 `json:"o"`
	H    float64 `json:"h"`
	L    float64 `json:"l"`
	C    float64 `json:"c"`
	V    float64 `json:"v"`
	Time string  `json:"t"`
}

// ParseBarMessage converts raw Alpaca bar JSON into a domain.MarketBar.
func (w *WSClient) ParseBarMessage(data []byte) (domain.MarketBar, error) {
	var ab alpacaBar
	if err := json.Unmarshal(data, &ab); err != nil {
		return domain.MarketBar{}, err
	}

	t, err := time.Parse(time.RFC3339, ab.Time)
	if err != nil {
		return domain.MarketBar{}, err
	}

	sym, err := domain.NewSymbol(ab.S)
	if err != nil {
		return domain.MarketBar{}, err
	}

	tf, _ := domain.NewTimeframe("1m")

	return domain.NewMarketBar(t, sym, tf, ab.O, ab.H, ab.L, ab.C, ab.V)
}

// StreamBars is a stub for streaming market bars.
func (w *WSClient) StreamBars(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, handler ports.BarHandler) error {
	return nil
}

// Close safely closes the WebSocket client connections.
func (w *WSClient) Close() error {
	w.closeOnce.Do(func() {
	})
	return nil
}
