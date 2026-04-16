package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────
// Domain types for REST responses
// ──────────────────────────────────────────────────────────────────────────

// AccountState represents the user's clearinghouse state on Hyperliquid.
type AccountState struct {
	MarginSummary MarginSummary      `json:"marginSummary"`
	Positions     []PositionState    `json:"assetPositions"`
	CrossMarginSummary *MarginSummary `json:"crossMarginSummary,omitempty"`
}

// MarginSummary holds margin information.
type MarginSummary struct {
	AccountValue    float64 `json:"-"`
	TotalNtlPos     float64 `json:"-"`
	TotalRawUsd     float64 `json:"-"`
	TotalMarginUsed float64 `json:"-"`
}

// marginSummaryRaw is the wire format (string numbers).
type marginSummaryRaw struct {
	AccountValue    string `json:"accountValue"`
	TotalNtlPos     string `json:"totalNtlPos"`
	TotalRawUsd     string `json:"totalRawUsd"`
	TotalMarginUsed string `json:"totalMarginUsed"`
}

func (m *MarginSummary) UnmarshalJSON(data []byte) error {
	var raw marginSummaryRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.AccountValue = parseFloat(raw.AccountValue)
	m.TotalNtlPos = parseFloat(raw.TotalNtlPos)
	m.TotalRawUsd = parseFloat(raw.TotalRawUsd)
	m.TotalMarginUsed = parseFloat(raw.TotalMarginUsed)
	return nil
}

// PositionState represents a single open position.
type PositionState struct {
	Position PositionData `json:"position"`
}

// PositionData holds the core position fields.
type PositionData struct {
	Coin           string  `json:"coin"`
	Szi            float64 `json:"-"` // signed size (positive=long, negative=short)
	EntryPx        float64 `json:"-"`
	PositionValue  float64 `json:"-"`
	UnrealizedPnl  float64 `json:"-"`
	Leverage       LeverageData `json:"leverage"`
	LiquidationPx  float64 `json:"-"`
	MarginUsed     float64 `json:"-"`
	ReturnOnEquity float64 `json:"-"`
}

type positionDataRaw struct {
	Coin           string       `json:"coin"`
	Szi            string       `json:"szi"`
	EntryPx        string       `json:"entryPx"`
	PositionValue  string       `json:"positionValue"`
	UnrealizedPnl  string       `json:"unrealizedPnl"`
	Leverage       LeverageData `json:"leverage"`
	LiquidationPx  string       `json:"liquidationPx"`
	MarginUsed     string       `json:"marginUsed"`
	ReturnOnEquity string       `json:"returnOnEquity"`
}

func (p *PositionData) UnmarshalJSON(data []byte) error {
	var raw positionDataRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Coin = raw.Coin
	p.Szi = parseFloat(raw.Szi)
	p.EntryPx = parseFloat(raw.EntryPx)
	p.PositionValue = parseFloat(raw.PositionValue)
	p.UnrealizedPnl = parseFloat(raw.UnrealizedPnl)
	p.Leverage = raw.Leverage
	p.LiquidationPx = parseFloat(raw.LiquidationPx)
	p.MarginUsed = parseFloat(raw.MarginUsed)
	p.ReturnOnEquity = parseFloat(raw.ReturnOnEquity)
	return nil
}

// LeverageData holds leverage info.
type LeverageData struct {
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

// FundingPayment represents a single historical funding payment.
type FundingPayment struct {
	Time    time.Time
	Coin    string
	UsdSize float64
	Rate    float64
}

type fundingPaymentRaw struct {
	Time    int64  `json:"time"` // epoch millis
	Coin    string `json:"coin"`
	UsdSize string `json:"usds"`
	Hash    string `json:"hash"`
	Delta   struct {
		Type      string `json:"type"`
		Coin      string `json:"coin"`
		UsdSize   string `json:"usds"`
		FundingRate string `json:"fundingRate"`
	} `json:"delta"`
}

// FundingRate represents a funding rate snapshot for an asset.
type FundingRate struct {
	Coin       string
	Rate       float64
	Premium    float64
	MarkPrice  float64
	OpenInterest float64
}

// Candle represents an OHLCV candle from the Hyperliquid API.
type Candle struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Trades int
}

type candleRaw struct {
	T int64  `json:"t"` // open time epoch millis
	O string `json:"o"`
	H string `json:"h"`
	L string `json:"l"`
	C string `json:"c"`
	V string `json:"v"`
	N int    `json:"n"` // trade count
}

// OpenInterest holds OI data for an asset.
type OpenInterest struct {
	Coin      string
	OI        float64
	OIUsd     float64
	MarkPrice float64
}

// RESTClient wraps the shared Client to provide typed methods for the
// Hyperliquid /info endpoint.
type RESTClient struct {
	client *Client
}

// NewRESTClient creates a RESTClient backed by the given shared Client.
func NewRESTClient(c *Client) *RESTClient {
	return &RESTClient{client: c}
}

// GetAccountState returns the clearinghouse state for the given address.
func (r *RESTClient) GetAccountState(ctx context.Context, address string) (AccountState, error) {
	body := map[string]string{
		"type": "clearinghouseState",
		"user": address,
	}
	raw, err := r.client.PostInfo(ctx, body)
	if err != nil {
		return AccountState{}, fmt.Errorf("hyperliquid: get account state: %w", err)
	}
	var state AccountState
	if err := json.Unmarshal(raw, &state); err != nil {
		return AccountState{}, fmt.Errorf("hyperliquid: unmarshal account state: %w", err)
	}
	return state, nil
}

// GetFundingHistory returns funding payment history for the given address.
func (r *RESTClient) GetFundingHistory(ctx context.Context, address string, startTime, endTime time.Time) ([]FundingPayment, error) {
	body := map[string]any{
		"type":      "userFunding",
		"user":      address,
		"startTime": startTime.UnixMilli(),
		"endTime":   endTime.UnixMilli(),
	}
	raw, err := r.client.PostInfo(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: get funding history: %w", err)
	}
	var rawPayments []fundingPaymentRaw
	if err := json.Unmarshal(raw, &rawPayments); err != nil {
		return nil, fmt.Errorf("hyperliquid: unmarshal funding history: %w", err)
	}
	payments := make([]FundingPayment, 0, len(rawPayments))
	for _, rp := range rawPayments {
		payments = append(payments, FundingPayment{
			Time:    time.UnixMilli(rp.Time),
			Coin:    rp.Delta.Coin,
			UsdSize: parseFloat(rp.Delta.UsdSize),
			Rate:    parseFloat(rp.Delta.FundingRate),
		})
	}
	return payments, nil
}

// MetaAndAssetCtxsResponse is the combined response from metaAndAssetCtxs.
// It is an array of two elements: [meta, [assetCtx...]].
type MetaAndAssetCtxsResponse struct {
	Meta      metaResponse
	AssetCtxs []assetCtx
}

type assetCtx struct {
	Funding   string `json:"funding"`
	MarkPx    string `json:"markPx"`
	OpenInterest string `json:"openInterest"`
	OraclePrice  string `json:"oraclePx"`
	Premium   string `json:"premium"`
}

// GetMetaAndAssetCtxs fetches the combined meta + asset context data.
func (r *RESTClient) GetMetaAndAssetCtxs(ctx context.Context) (*MetaAndAssetCtxsResponse, error) {
	body := map[string]string{
		"type": "metaAndAssetCtxs",
	}
	raw, err := r.client.PostInfo(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: get metaAndAssetCtxs: %w", err)
	}

	// Response is a JSON array of 2 elements: [meta, [assetCtx...]]
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("hyperliquid: unmarshal metaAndAssetCtxs: %w", err)
	}
	if len(arr) < 2 {
		return nil, fmt.Errorf("%w: metaAndAssetCtxs returned %d elements, expected 2", ErrInvalidResponse, len(arr))
	}

	var meta metaResponse
	if err := json.Unmarshal(arr[0], &meta); err != nil {
		return nil, fmt.Errorf("hyperliquid: unmarshal meta part: %w", err)
	}
	var ctxs []assetCtx
	if err := json.Unmarshal(arr[1], &ctxs); err != nil {
		return nil, fmt.Errorf("hyperliquid: unmarshal assetCtxs part: %w", err)
	}

	return &MetaAndAssetCtxsResponse{Meta: meta, AssetCtxs: ctxs}, nil
}

// GetFundingRates returns the current funding rates for all assets.
func (r *RESTClient) GetFundingRates(ctx context.Context) ([]FundingRate, error) {
	resp, err := r.GetMetaAndAssetCtxs(ctx)
	if err != nil {
		return nil, err
	}

	rates := make([]FundingRate, 0, len(resp.AssetCtxs))
	for i, ac := range resp.AssetCtxs {
		var coin string
		if i < len(resp.Meta.Universe) {
			coin = resp.Meta.Universe[i].Name
		}
		rates = append(rates, FundingRate{
			Coin:         coin,
			Rate:         parseFloat(ac.Funding),
			Premium:      parseFloat(ac.Premium),
			MarkPrice:    parseFloat(ac.MarkPx),
			OpenInterest: parseFloat(ac.OpenInterest),
		})
	}
	return rates, nil
}

// GetCandles fetches historical candles for a coin.
func (r *RESTClient) GetCandles(ctx context.Context, coin, interval string, startTime, endTime time.Time) ([]Candle, error) {
	body := map[string]any{
		"type": "candleSnapshot",
		"req": map[string]any{
			"coin":      coin,
			"interval":  interval,
			"startTime": startTime.UnixMilli(),
			"endTime":   endTime.UnixMilli(),
		},
	}
	raw, err := r.client.PostInfo(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: get candles: %w", err)
	}
	var rawCandles []candleRaw
	if err := json.Unmarshal(raw, &rawCandles); err != nil {
		return nil, fmt.Errorf("hyperliquid: unmarshal candles: %w", err)
	}
	candles := make([]Candle, 0, len(rawCandles))
	for _, rc := range rawCandles {
		candles = append(candles, Candle{
			Time:   time.UnixMilli(rc.T),
			Open:   parseFloat(rc.O),
			High:   parseFloat(rc.H),
			Low:    parseFloat(rc.L),
			Close:  parseFloat(rc.C),
			Volume: parseFloat(rc.V),
			Trades: rc.N,
		})
	}
	return candles, nil
}

// GetOpenInterest returns the OI for a specific coin from metaAndAssetCtxs.
func (r *RESTClient) GetOpenInterest(ctx context.Context, coin string) (OpenInterest, error) {
	resp, err := r.GetMetaAndAssetCtxs(ctx)
	if err != nil {
		return OpenInterest{}, err
	}
	for i, u := range resp.Meta.Universe {
		if u.Name == coin && i < len(resp.AssetCtxs) {
			ac := resp.AssetCtxs[i]
			markPx := parseFloat(ac.MarkPx)
			oi := parseFloat(ac.OpenInterest)
			return OpenInterest{
				Coin:      coin,
				OI:        oi,
				OIUsd:     oi * markPx,
				MarkPrice: markPx,
			}, nil
		}
	}
	return OpenInterest{}, fmt.Errorf("%w: %s", ErrAssetNotFound, coin)
}

// PublicFundingSnapshot represents a single historical funding rate from the
// public fundingHistory endpoint (no wallet required).
type PublicFundingSnapshot struct {
	Coin string
	Time time.Time
	Rate float64
}

type publicFundingRaw struct {
	Coin        string `json:"coin"`
	FundingRate string `json:"fundingRate"`
	Premium     string `json:"premium"`
	Time        int64  `json:"time"` // epoch millis
}

// GetPublicFundingHistory fetches the public per-asset funding rate history.
// Unlike GetFundingHistory, this does not require a wallet address.
// The endpoint returns all assets' rates; filtering by coin is done client-side.
func (r *RESTClient) GetPublicFundingHistory(ctx context.Context, coin string, startTime, endTime time.Time) ([]PublicFundingSnapshot, error) {
	body := map[string]any{
		"type":      "fundingHistory",
		"coin":      coin,
		"startTime": startTime.UnixMilli(),
		"endTime":   endTime.UnixMilli(),
	}
	raw, err := r.client.PostInfo(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: get public funding history: %w", err)
	}
	var rawEntries []publicFundingRaw
	if err := json.Unmarshal(raw, &rawEntries); err != nil {
		return nil, fmt.Errorf("hyperliquid: unmarshal public funding history: %w", err)
	}
	result := make([]PublicFundingSnapshot, 0, len(rawEntries))
	for _, e := range rawEntries {
		result = append(result, PublicFundingSnapshot{
			Coin: e.Coin,
			Time: time.UnixMilli(e.Time),
			Rate: parseFloat(e.FundingRate),
		})
	}
	return result, nil
}

// GetOpenOrders returns all open orders for the given address.
func (r *RESTClient) GetOpenOrders(ctx context.Context, address string) ([]OrderResponse, error) {
	body := map[string]string{
		"type": "openOrders",
		"user": address,
	}
	raw, err := r.client.PostInfo(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: get open orders: %w", err)
	}
	var orders []OrderResponse
	if err := json.Unmarshal(raw, &orders); err != nil {
		return nil, fmt.Errorf("hyperliquid: unmarshal open orders: %w", err)
	}
	return orders, nil
}

// OrderResponse is the shape of an order from the openOrders endpoint.
type OrderResponse struct {
	Coin      string `json:"coin"`
	OID       int64  `json:"oid"`
	Side      string `json:"side"` // "A" (ask/sell) or "B" (bid/buy)
	LimitPx   string `json:"limitPx"`
	Sz        string `json:"sz"`
	Timestamp int64  `json:"timestamp"`
	OrderType string `json:"orderType,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────

// parseFloat converts a string to float64, returning 0 on failure.
func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
