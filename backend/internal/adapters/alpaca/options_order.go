package alpaca

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// SubmitOptionOrder submits an option order to the Alpaca REST API.
// MVP rules:
//   - Only buying options is supported (DirectionLong). DirectionShort returns an error.
//   - time_in_force is always "day" for options.
//   - No stop_price in the request body (risk controlled via MaxLossUSD).
//   - symbol is the OCC contract symbol from intent.Instrument.Symbol.
func (c *RESTClient) SubmitOptionOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	if intent.Direction == domain.DirectionShort {
		return "", errors.New("alpaca: MVP does not support selling options")
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
		"side":          "buy",
		"type":          "limit",
		"time_in_force": "day",
		"limit_price":   intent.LimitPrice,
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
