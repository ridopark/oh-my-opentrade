package alpaca

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"

	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// SubmitOptionOrder submits an option order to the Alpaca REST API.
// Direction mapping:
//   - DirectionLong  → side="buy"  (buy to open)
//   - DirectionShort → side="sell" (sell to close / defined-risk spread leg)
//
// time_in_force is always "day" for options.
// No stop_price in the request body (risk controlled via MaxLossUSD).
// symbol is the OCC contract symbol from intent.Instrument.Symbol.
func (c *RESTClient) SubmitOptionOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	// Map direction to Alpaca order side.
	side := "buy"
	if intent.Direction == domain.DirectionShort {
		side = "sell"
	}

	sym := ""
	if intent.Instrument != nil {
		sym = intent.Instrument.Symbol.String()
	} else {
		sym = intent.Symbol.String()
	}

	reqBody := map[string]interface{}{
		"symbol":        sym,
		"qty":           fmt.Sprintf("%g", intent.Quantity),
		"side":          side,
		"type":          "limit",
		"time_in_force": "day",
		"limit_price":   math.Round(intent.LimitPrice*100) / 100,
	}

	b, _ := json.Marshal(reqBody)
	resp, err := c.doReq(ctx, http.MethodPost, pathOrders, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("alpaca: submit option order failed (status %d): %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&res); err != nil {
		return "", err
	}
	return res.ID, nil
}
