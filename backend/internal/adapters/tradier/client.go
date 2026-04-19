// Package tradier is a minimal REST client for the Tradier sandbox API,
// scoped to the one task we need: daily snapshotting of option chain
// expirations + contracts with Greeks/IV for symbols DoltHub doesn't cover.
//
// The client outputs domain.HistoricalOptionChainRow so its writes can go
// into the same historical_option_chain table the DoltHub importer uses.
// This lets the simbroker consume Tradier-sourced data through the existing
// HistoricalOptionsPort without any downstream changes.
package tradier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// Config tunes the Tradier REST client.
type Config struct {
	Token       string        // Bearer token from developer.tradier.com
	BaseURL     string        // Defaults to https://sandbox.tradier.com/v1
	HTTPTimeout time.Duration // Defaults to 20s
}

// Client is a minimal Tradier REST adapter. Sandbox is the default target
// because (a) it has option-chain-with-Greeks endpoints on the free tier,
// and (b) we only need EOD-quality snapshots — no real-time stream.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
	log     zerolog.Logger
}

// NewClient constructs a Client. Returns nil when Token is empty so callers
// can wire it unconditionally and have snapshot jobs become no-ops when
// Tradier isn't configured yet. Mirrors the thetadata adapter's pattern.
func NewClient(cfg Config, log zerolog.Logger) *Client {
	if cfg.Token == "" {
		log.Debug().Msg("tradier: no token configured; client disabled")
		return nil
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://sandbox.tradier.com/v1"
	}
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	return &Client{
		token:   cfg.Token,
		baseURL: base,
		http:    &http.Client{Timeout: timeout},
		log:     log.With().Str("component", "tradier").Logger(),
	}
}

// Expirations returns the list of expiration dates Tradier exposes for the
// given underlying on the current session. Matches the symbols the options
// chain endpoint accepts.
func (c *Client) Expirations(ctx context.Context, symbol string) ([]time.Time, error) {
	path := fmt.Sprintf("/markets/options/expirations?symbol=%s&includeAllRoots=true",
		symbol)
	var env struct {
		Expirations struct {
			Date []string `json:"date"`
		} `json:"expirations"`
	}
	if err := c.get(ctx, path, &env); err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, len(env.Expirations.Date))
	for _, s := range env.Expirations.Date {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			c.log.Warn().Err(err).Str("date", s).Msg("skip unparseable expiration")
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// ChainSnapshot returns all option contracts for the given underlying and
// expiration, expressed as HistoricalOptionChainRow values dated "today"
// (snapshot semantics). Greeks and IV are populated from Tradier's embedded
// `greeks` object on each contract.
func (c *Client) ChainSnapshot(ctx context.Context, symbol string, expiration time.Time) ([]domain.HistoricalOptionChainRow, error) {
	date := expiration.Format("2006-01-02")
	path := fmt.Sprintf("/markets/options/chains?symbol=%s&expiration=%s&greeks=true",
		symbol, date)

	// Tradier wraps contracts in options.option which can be either an
	// object (single contract) or an array (normal case). Decode into a
	// RawMessage first and handle both shapes.
	var env struct {
		Options struct {
			Option json.RawMessage `json:"option"`
		} `json:"options"`
	}
	if err := c.get(ctx, path, &env); err != nil {
		return nil, err
	}

	var contracts []tradierContract
	if len(env.Options.Option) == 0 || string(env.Options.Option) == "null" {
		return nil, nil
	}
	if env.Options.Option[0] == '[' {
		if err := json.Unmarshal(env.Options.Option, &contracts); err != nil {
			return nil, fmt.Errorf("tradier: decode chain array: %w", err)
		}
	} else {
		var single tradierContract
		if err := json.Unmarshal(env.Options.Option, &single); err != nil {
			return nil, fmt.Errorf("tradier: decode chain single: %w", err)
		}
		contracts = []tradierContract{single}
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	rows := make([]domain.HistoricalOptionChainRow, 0, len(contracts))
	for _, tc := range contracts {
		right := domain.OptionRightCall
		if strings.EqualFold(tc.OptionType, "put") {
			right = domain.OptionRightPut
		}
		rows = append(rows, domain.HistoricalOptionChainRow{
			Date:       today,
			Symbol:     domain.Symbol(tc.Underlying),
			Expiration: expiration,
			Strike:     tc.Strike,
			Right:      right,
			Bid:        tc.Bid,
			Ask:        tc.Ask,
			IV:         tc.Greeks.MidIV,
			Delta:      tc.Greeks.Delta,
			Gamma:      tc.Greeks.Gamma,
			Theta:      tc.Greeks.Theta,
			Vega:       tc.Greeks.Vega,
			Rho:        tc.Greeks.Rho,
		})
	}
	return rows, nil
}

// get is the single shared HTTP helper. Tradier uses Bearer auth and returns
// JSON with "application/json" Accept header.
func (c *Client) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("tradier: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tradier: http: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tradier: HTTP %d: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("tradier: decode: %w (body: %s)", err, truncate(string(body), 200))
	}
	return nil
}

// tradierContract is the subset of fields we consume from the
// /markets/options/chains response. Tradier returns far more; we ignore the
// rest because the DoltHub-compatible schema only needs these columns.
type tradierContract struct {
	Symbol      string  `json:"symbol"`
	Underlying  string  `json:"underlying"`
	Strike      float64 `json:"strike"`
	OptionType  string  `json:"option_type"`
	Bid         float64 `json:"bid"`
	Ask         float64 `json:"ask"`
	Greeks      greeks  `json:"greeks"`
}

// greeks mirrors Tradier's `greeks` object. MidIV is the IV field we
// persist as the canonical "iv" value in HistoricalOptionChainRow —
// Tradier also exposes bid_iv/ask_iv but mid_iv is the fairest single
// number for backtest-replay purposes.
type greeks struct {
	Delta float64 `json:"delta"`
	Gamma float64 `json:"gamma"`
	Theta float64 `json:"theta"`
	Vega  float64 `json:"vega"`
	Rho   float64 `json:"rho"`
	MidIV float64 `json:"mid_iv"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
