package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

func (c *RESTClient) GetSnapshots(ctx context.Context, dataURL string, symbols []string) (map[string]ports.Snapshot, error) {
	if len(symbols) == 0 {
		return map[string]ports.Snapshot{}, nil
	}

	path := "/v2/stocks/snapshots?symbols=" + strings.Join(symbols, ",") + "&feed=" + c.equityFeed()
	resp, err := c.doReqDataAPI(ctx, dataURL, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
	if err != nil {
		c.log.Error().Err(err).Msg("snapshots HTTP request failed")
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alpaca: get snapshots failed (status %d): %s", resp.StatusCode, string(body))
	}

	respBody := bytes.NewReader(body)
	var raw map[string]struct {
		LatestTrade *struct {
			P *float64 `json:"p"`
		} `json:"latestTrade"`
		PrevDailyBar *struct {
			C *float64 `json:"c"`
		} `json:"prevDailyBar"`
		MinuteBar *struct {
			C *float64 `json:"c"`
			V *int64   `json:"v"`
		} `json:"minuteBar"`
		DailyBar *struct {
			V *int64 `json:"v"`
		} `json:"dailyBar"`
	}
	if err := json.NewDecoder(respBody).Decode(&raw); err != nil {
		return nil, fmt.Errorf("alpaca: decode snapshots: %w", err)
	}

	out := make(map[string]ports.Snapshot, len(raw))
	for sym, r := range raw {
		var lastTrade *float64
		if r.LatestTrade != nil {
			lastTrade = r.LatestTrade.P
		}
		var prevClose *float64
		if r.PrevDailyBar != nil {
			prevClose = r.PrevDailyBar.C
		}
		var pmPrice *float64
		var pmVol *int64
		if r.MinuteBar != nil {
			pmPrice = r.MinuteBar.C
			pmVol = r.MinuteBar.V
		}

		out[sym] = ports.Snapshot{
			Symbol:          sym,
			PrevClose:       prevClose,
			PreMarketPrice:  pmPrice,
			PreMarketVolume: pmVol,
			LastTradePrice:  lastTrade,
		}
	}
	return out, nil
}
