package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the complete application configuration.
type Config struct {
	Alpaca       AlpacaConfig       `yaml:"alpaca"`
	Coinbase     CoinbaseConfig     `yaml:"coinbase"`
	IBKR         IBKRConfig         `yaml:"ibkr"`
	Hyperliquid  HyperliquidConfig  `yaml:"hyperliquid"`
	Bybit        BybitConfig        `yaml:"bybit"`
	StreamingSource string          `yaml:"-"` // "alpaca" or "ibkr"; defaults to "alpaca"
	Database     DatabaseConfig     `yaml:"database"`
	Trading      TradingConfig      `yaml:"trading"`
	Symbols      SymbolsConfig      `yaml:"symbols"`
	Server       ServerConfig       `yaml:"server"`
	AI           AIConfig           `yaml:"ai"`
	AIScreener   AIScreenerConfig   `yaml:"ai_screener"`
	Notification NotificationConfig `yaml:"notification"`
	Backtest     BacktestConfig     `yaml:"backtest"`
	Options      OptionsConfig      `yaml:"options"`
	Deribit      DeribitConfig      `yaml:"deribit"`
	OnChain      OnChainConfig      `yaml:"onchain"`
	Risk         RiskConfig         `yaml:"risk"`
	Exits        ExitsConfig        `yaml:"exits"`
	OptionsV2    bool               `yaml:"-"`
	MultiAccount bool               `yaml:"-"`
	// OrderJournalEnabled toggles the Sprint 2 write-ahead order-intent
	// journal and journal-aware startup reconciliation. Default off (legacy
	// behavior — cancel-all on startup, no intent persistence) so production
	// deploys can ship the code and flip the flag independently.
	OrderJournalEnabled bool `yaml:"-"`
}

// HyperliquidConfig holds connection and authentication parameters for the
// Hyperliquid perpetual exchange adapter. Disabled by default — PrivateKey
// must be set to activate trading, and Network defaults to "testnet" when
// empty so accidental mainnet orders are impossible without explicit opt-in.
type HyperliquidConfig struct {
	Network      string `yaml:"network"`       // "mainnet" or "testnet"; defaults to "testnet"
	PrivateKey   string `yaml:"private_key"`    // hex-encoded Ethereum private key (without 0x prefix)
	Address      string `yaml:"address"`        // 0x... address; derived from private key if empty
	VaultAddress string `yaml:"vault_address"`  // optional vault for sub-account trading
}

// IBKRConfig holds connection parameters for the IB Gateway adapter.
type IBKRConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	ClientID       int    `yaml:"client_id"`
	PaperMode      bool   `yaml:"paper_mode"`
	AccountID      string `yaml:"account_id"`
	MarketDataType int    `yaml:"market_data_type"`
}

// AlpacaConfig represents the Alpaca broker configuration.
type AlpacaConfig struct {
	APIKeyID      string `yaml:"api_key_id"`
	APISecretKey  string `yaml:"api_secret_key"`
	BaseURL       string `yaml:"base_url"`
	DataURL       string `yaml:"data_url"`
	Feed          string `yaml:"feed"`
	PaperMode     bool   `yaml:"paper_mode"`
	CryptoDataURL string `yaml:"crypto_data_url"`
	CryptoFeed    string `yaml:"crypto_feed"`
}

// CoinbaseConfig holds parameters for the read-only Coinbase Exchange public
// market-data adapter. Only the /products/{id}/candles endpoint is used so no
// API credentials are needed. Defaults are seeded in Load when the YAML
// omits the section entirely so the adapter works out of the box for crypto
// backfills replacing Alpaca's low-volume US crypto feed.
type CoinbaseConfig struct {
	BaseURL        string `yaml:"base_url"`         // default https://api.exchange.coinbase.com
	RateLimitRPS   int    `yaml:"rate_limit_rps"`   // default 8 (public cap is 10 rps)
	TimeoutSeconds int    `yaml:"timeout_seconds"`  // default 30
}

// AIConfig holds configuration for the AI adversarial debate system.
type AIConfig struct {
	BaseURL        string  `yaml:"base_url"`
	Model          string  `yaml:"model"`
	BacktestModel  string  `yaml:"backtest_model"` // cheaper/free model for backtests; falls back to Model when empty
	APIKey         string  `yaml:"api_key"`
	MinConfidence  float64 `yaml:"min_confidence"`
	Enabled        bool    `yaml:"enabled"`
	ProviderSort   string  `yaml:"provider_sort"` // OpenRouter provider routing sort (e.g. "latency")
}

type AIScreenerConfig struct {
	Enabled              bool     `yaml:"enabled"`
	Models               []string `yaml:"models"`
	NumericRunAtHourET   int      `yaml:"numeric_run_at_hour_et"`
	NumericRunAtMinuteET int      `yaml:"numeric_run_at_minute_et"`
	AIRunAtHourET        int      `yaml:"ai_run_at_hour_et"`
	AIRunAtMinuteET      int      `yaml:"ai_run_at_minute_et"`
	Pass0MinPrice        float64  `yaml:"pass0_min_price"`
	Pass0MinVolume       int64    `yaml:"pass0_min_volume"`
	Pass0MinADV          int64    `yaml:"pass0_min_adv"`
	Pass0MinGapPct       float64  `yaml:"pass0_min_gap_pct"`
	Pass0MinATRPct       float64  `yaml:"pass0_min_atr_pct"` // skip symbols with daily ATR% below this (0 = disabled)
	MaxCandidatesPerCall int      `yaml:"max_candidates_per_call"`
	TopNPerStrategy      int      `yaml:"top_n_per_strategy"`
}

// NotificationConfig holds credentials for notification adapters.
type NotificationConfig struct {
	TelegramBotToken  string `yaml:"telegram_bot_token"`
	TelegramChatID    string `yaml:"telegram_chat_id"`
	DiscordWebhookURL string `yaml:"discord_webhook_url"`
	KakaoRestAPIKey   string `yaml:"kakao_rest_api_key"`
	KakaoRedirectURI  string `yaml:"kakao_redirect_uri"`
}

// DatabaseConfig represents the database connection configuration.
type DatabaseConfig struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	User        string `yaml:"user"`
	Password    string `yaml:"password"`
	DBName      string `yaml:"dbname"`
	SSLMode     string `yaml:"ssl_mode"`
	MaxPoolSize int    `yaml:"max_pool_size"`
}

// TradingConfig represents the trading rules and parameters.
type TradingConfig struct {
	MaxRiskPercent         float64           `yaml:"max_risk_percent"`
	DefaultSlippageBPS     int               `yaml:"default_slippage_bps"`
	KillSwitchMaxStops     int               `yaml:"kill_switch_max_stops"`
	KillSwitchWindow       time.Duration     `yaml:"-"`
	KillSwitchHaltDuration time.Duration     `yaml:"-"`
	MaxDailyLossPct        float64           `yaml:"max_daily_loss_pct"`
	MaxDailyLossUSD        float64           `yaml:"max_daily_loss_usd"`
	MaxSimultaneousPos     int               `yaml:"max_simultaneous_positions"`
	MaxPositionsPerGroup   int               `yaml:"max_positions_per_group"`
	// MaxPortfolioHeat caps aggregate risk (Σ |entry-stop|*qty) across
	// all open positions as a fraction of account equity. 0 = disabled
	// (default); 0.10 activates the gate at 10% heat.
	MaxPortfolioHeat float64           `yaml:"max_portfolio_heat"`
	// MaxSectorExposure caps the notional share of any single GICS sector
	// as a fraction of account equity. 0 = disabled (default); 0.30
	// activates the gate at 30% per-sector concentration.
	MaxSectorExposure float64 `yaml:"max_sector_exposure"`
	// MaxIndustryExposure caps the notional share of any single GICS
	// industry. 0 = disabled; 0.20 activates at 20%.
	MaxIndustryExposure float64 `yaml:"max_industry_exposure"`
	// MaxDirectionalBias caps |Σ long − Σ short| / equity across open
	// non-option positions. 0 = disabled (default); 0.70 activates the
	// gate at 70% net-directional exposure. Bias-reducing intents are
	// always allowed — only bias-increasing intents are gated.
	MaxDirectionalBias float64 `yaml:"max_directional_bias"`
	// SymbolMetadataPath points to the GICS sector/industry TOML file used
	// by the sector_exposure gate. Empty disables metadata loading.
	SymbolMetadataPath string            `yaml:"symbol_metadata_path"`
	OptionsRisk        OptionsRiskConfig `yaml:"options_risk"`
	// Sprint 4.5 compliance — default-disabled so legacy deploys behave
	// exactly as before until operators opt in.
	//
	// PDTEnforcement: "strict" enables pdt_guard (requires
	// PatternDayTrader=true AND equity<25k to actually block); "off"
	// disables the gate unconditionally. Empty string = "off".
	PDTEnforcement string `yaml:"pdt_enforcement"`
	// RegTEnforcement enables the Reg-T 50% initial-margin gate. Default
	// false; intended to be set true only when running on IBKR (paper or
	// live). Simbroker / Alpaca paper skip this regardless.
	RegTEnforcement bool `yaml:"reg_t_enforcement"`
	// Sprint 4.6 — earnings blackout per strategy. Keys are strategy
	// names; values are "strict", "permissive", or "off". Missing
	// entries default to "off" so legacy strategies are unaffected.
	EarningsBlackout map[string]string `yaml:"earnings_blackout"`
	// MacroEventBlackoutMinutes is the half-window (minutes) around a
	// high-impact macro release during which new entries are rejected.
	// Default 30 when zero; set to a negative value to force default.
	MacroEventBlackoutMinutes int `yaml:"macro_event_blackout_minutes"`
	// MacroEventImpacts lists the impact tags that trigger a blackout.
	// Default ["high"] when empty.
	MacroEventImpacts []string `yaml:"macro_event_impacts"`
}

// BacktestConfig holds Sprint-7 fill-model and fee-schedule knobs. Empty
// values fall back to defaults that preserve today's backtest numbers
// (optimistic fills, no fees) unless operators opt in to realism.
type BacktestConfig struct {
	FillModel                     string  `yaml:"fill_model"`                      // "optimistic" | "realistic" | "pessimistic"
	LatencyMsEquity               int     `yaml:"latency_ms_equity"`               // default 50
	LatencyMsOption               int     `yaml:"latency_ms_option"`               // default 200
	FeeSchedule                   string  `yaml:"fee_schedule"`                    // "alpaca_equity" | "ibkr_options" | "none"
	PessimisticSlippageMultiplier float64 `yaml:"pessimistic_slippage_multiplier"` // default 2.0

	// EnforceUniverseHistory enables the Sprint-7-addon survivorship-bias
	// filter: when true, the backtest bar loader consults a
	// UniverseHistoryPort and drops bars (or skips symbols entirely) for
	// intervals during which the symbol was not tradable. The flag
	// defaults to false so existing backtests that operated on the
	// always-current active-universe list continue to reproduce their
	// published numbers bit-for-bit until an operator explicitly opts
	// in. If the flag is true but no UniverseHistoryPort is wired, the
	// runner logs a warning and proceeds without filtering rather than
	// failing closed.
	EnforceUniverseHistory bool `yaml:"enforce_universe_history"`
}

// OptionsConfig groups options-pipeline knobs that live outside the
// per-strategy spec. UseLiveMarketData is the master switch for the
// Theta Data integration: when false (the default), no live client is
// instantiated and the existing BSM/ATR synthetic IV path runs unchanged.
type OptionsConfig struct {
	UseLiveMarketData bool             `yaml:"use_live_market_data"`
	ThetaData         ThetaDataConfig  `yaml:"theta_data"`
}

// ThetaDataConfig holds the credentials and rate-limit cap for the
// Theta Data REST snapshot client. Empty APIKey leaves the adapter
// uninstantiated even when UseLiveMarketData is true.
type ThetaDataConfig struct {
	APIKey          string `yaml:"api_key"`
	BaseURL         string `yaml:"base_url"`
	RateLimitPerSec int    `yaml:"rate_limit_per_sec"`
}

// OnChainConfig holds parameters for the read-only on-chain whale/custodian
// flow adapter powered by Dune Analytics. Disabled by default: Enabled must
// be set to true and DuneAPIKey must be non-empty to activate.
type OnChainConfig struct {
	DuneAPIKey  string         `yaml:"dune_api_key"`
	DuneBaseURL string         `yaml:"dune_base_url"` // default https://api.dune.com/api/v1/
	QueryIDs    map[string]int `yaml:"query_ids"`     // asset -> Dune query ID
	CacheTTL    string         `yaml:"cache_ttl"`     // default "5m"
	Enabled     bool           `yaml:"enabled"`       // default false
}

// BybitConfig holds parameters for the read-only Bybit funding rate adapter.
// Disabled by default: Enabled must be set to true to activate the adapter.
// No authentication is needed (public endpoints only).
type BybitConfig struct {
	BaseURL string `yaml:"base_url"` // default https://api.bybit.com
	Enabled bool   `yaml:"enabled"`  // default false
}

// DeribitConfig holds parameters for the read-only Deribit options IV surface
// adapter. Disabled by default: an empty Assets list means no polling occurs.
// No authentication is needed (public endpoints only).
type DeribitConfig struct {
	BaseURL      string   `yaml:"base_url"`      // default https://www.deribit.com/api/v2/
	PollInterval string   `yaml:"poll_interval"` // default "5m"
	Assets       []string `yaml:"assets"`        // default ["BTC", "ETH"]
}

type OptionsRiskConfig struct {
	MinOpenInterest int     `yaml:"min_open_interest"`
	MaxSpreadPct    float64 `yaml:"max_spread_pct"`
	MaxIVCeiling    float64 `yaml:"max_iv_ceiling"`
	MinDTE          int     `yaml:"min_dte"`
}

// SymbolGroupConfig represents a group of symbols sharing the same asset class and timeframe.
type SymbolGroupConfig struct {
	AssetClass string   `yaml:"asset_class"`
	Symbols    []string `yaml:"symbols"`
	Timeframe  string   `yaml:"timeframe"`
}

// SymbolsConfig represents the symbols to trade and their timeframe.
type SymbolsConfig struct {
	Groups    []SymbolGroupConfig `yaml:"groups,omitempty"`
	Symbols   []string            `yaml:"symbols,omitempty"`   // backward compat
	Timeframe string              `yaml:"timeframe,omitempty"` // backward compat
}

// Normalize migrates flat Symbols/Timeframe into Groups for backward compat.
// If Groups is already populated, it populates the flat Symbols field from Groups.
// If only Symbols is set, it wraps them in a single EQUITY group.
func (sc *SymbolsConfig) Normalize() {
	if len(sc.Groups) > 0 {
		// Reverse-populate flat Symbols from Groups for backward compat.
		if len(sc.Symbols) == 0 {
			sc.Symbols = sc.AllSymbols()
			if sc.Timeframe == "" && len(sc.Groups) > 0 {
				sc.Timeframe = sc.Groups[0].Timeframe
			}
		}
		return
	}
	if len(sc.Symbols) > 0 {
		sc.Groups = []SymbolGroupConfig{
			{
				AssetClass: "EQUITY",
				Symbols:    sc.Symbols,
				Timeframe:  sc.Timeframe,
			},
		}
	}
}

// SymbolsByAssetClass returns symbols matching the given asset class.
func (sc *SymbolsConfig) SymbolsByAssetClass(ac string) []string {
	var result []string
	for _, g := range sc.Groups {
		if g.AssetClass == ac {
			result = append(result, g.Symbols...)
		}
	}
	return result
}

// AllSymbols returns all symbols across all groups.
func (sc *SymbolsConfig) AllSymbols() []string {
	var result []string
	for _, g := range sc.Groups {
		result = append(result, g.Symbols...)
	}
	return result
}

// ServerConfig represents the HTTP server configuration.
type ServerConfig struct {
	Port     int    `yaml:"port"`
	LogLevel string `yaml:"log_level"`
}

// rawTradingConfig represents the unparsed trading configuration.
type rawTradingConfig struct {
	MaxRiskPercent         float64           `yaml:"max_risk_percent"`
	DefaultSlippageBPS     int               `yaml:"default_slippage_bps"`
	KillSwitchMaxStops     int               `yaml:"kill_switch_max_stops"`
	KillSwitchWindow       string            `yaml:"kill_switch_window"`
	KillSwitchHaltDuration string            `yaml:"kill_switch_halt_duration"`
	MaxDailyLossPct        float64           `yaml:"max_daily_loss_pct"`
	MaxDailyLossUSD        float64           `yaml:"max_daily_loss_usd"`
	MaxSimultaneousPos     int               `yaml:"max_simultaneous_positions"`
	MaxPositionsPerGroup   int               `yaml:"max_positions_per_group"`
	MaxPortfolioHeat       float64           `yaml:"max_portfolio_heat"`
	MaxSectorExposure      float64           `yaml:"max_sector_exposure"`
	MaxIndustryExposure    float64           `yaml:"max_industry_exposure"`
	MaxDirectionalBias     float64           `yaml:"max_directional_bias"`
	SymbolMetadataPath     string            `yaml:"symbol_metadata_path"`
	OptionsRisk            OptionsRiskConfig `yaml:"options_risk"`
	PDTEnforcement         string            `yaml:"pdt_enforcement"`
	RegTEnforcement        bool              `yaml:"reg_t_enforcement"`
	EarningsBlackout       map[string]string `yaml:"earnings_blackout"`
	MacroEventBlackoutMinutes int            `yaml:"macro_event_blackout_minutes"`
	MacroEventImpacts      []string          `yaml:"macro_event_impacts"`
}

type rawConfig struct {
	Alpaca       AlpacaConfig       `yaml:"alpaca"`
	Coinbase     CoinbaseConfig     `yaml:"coinbase"`
	IBKR         IBKRConfig         `yaml:"ibkr"`
	Hyperliquid  HyperliquidConfig  `yaml:"hyperliquid"`
	Bybit        BybitConfig        `yaml:"bybit"`
	Database     DatabaseConfig     `yaml:"database"`
	Trading      rawTradingConfig   `yaml:"trading"`
	Symbols      SymbolsConfig      `yaml:"symbols"`
	Server       ServerConfig       `yaml:"server"`
	AI           AIConfig           `yaml:"ai"`
	AIScreener   AIScreenerConfig   `yaml:"ai_screener"`
	Notification NotificationConfig `yaml:"notification"`
	Backtest     BacktestConfig     `yaml:"backtest"`
	Options      OptionsConfig      `yaml:"options"`
	Deribit      DeribitConfig      `yaml:"deribit"`
	OnChain      OnChainConfig      `yaml:"onchain"`
	Risk         RiskConfig         `yaml:"risk"`
	Exits        ExitsConfig        `yaml:"exits"`
}

// RiskConfig groups cross-cutting risk gates that run outside the per-
// strategy DNA. Today it carries the per-position expected-loss cap
// (see PositionRiskCapConfig). Default-on, defense-in-depth for
// high-premium options that slip through notional sizing.
type RiskConfig struct {
	PositionCap PositionRiskCapConfig `yaml:"position_cap"`
}

// PositionRiskCapConfig bounds a single trade's expected loss at stop
// against a fraction of the daily-loss budget. When Mode is
// "account_pct", the budget is live-equity × DailyLossBudgetPct at
// sizing time (no state duplicated with DailyLossBreaker). When
// "fixed_usd", DailyLossBudgetUSD is used as-is.
//
// Defaults ship ENABLED per quant; the byte-identical-when-Enabled=false
// invariant is preserved so operators can kill-switch the cap.
type PositionRiskCapConfig struct {
	Enabled               bool    `yaml:"enabled"`
	Mode                  string  `yaml:"mode"`                     // "account_pct" | "fixed_usd"
	DailyLossBudgetPct    float64 `yaml:"daily_loss_budget_pct"`    // 0.0025 = 25 bps
	DailyLossBudgetUSD    float64 `yaml:"daily_loss_budget_usd"`    // fallback when Mode=fixed_usd
	MaxPositionRiskFrac   float64 `yaml:"max_position_risk_frac"`   // 0.20 = 20% of daily budget per trade
	StopPctSource         string  `yaml:"stop_pct_source"`          // only "widest_active" supported in phase 1
	ConfluenceBonusEnabled bool    `yaml:"confluence_bonus_enabled"`
	ConfluenceBonusMax    float64 `yaml:"confluence_bonus_max"`
	RejectOnFloor         bool    `yaml:"reject_on_floor"`
	AppliesTo             []string `yaml:"applies_to"`               // ["options"] in phase 1
}

// ExitsConfig groups exit-engine cross-cutting knobs. Today it carries
// the ATR-bucketed premium-trail multiplier (see ATRTrailConfig).
type ExitsConfig struct {
	ATRTrail ATRTrailConfig `yaml:"atr_trail"`
}

// ATRTrailConfig scales PREMIUM_TRAIL.trail_pct by an ATR%-percentile
// bucket. ATR% is computed per-symbol from daily bars over a rolling
// window; the tercile cutoffs and multipliers are exposed so quant can
// sweep them without code changes. Disabled defaults preserve
// byte-identical exit prices.
type ATRTrailConfig struct {
	Enabled                       bool      `yaml:"enabled"`
	ATRPeriod                     int       `yaml:"atr_period"`
	ATRTimeframe                  string    `yaml:"atr_timeframe"`
	ATRLookbackDays               int       `yaml:"atr_lookback_days"`
	ATRLookbackDaysCrypto         int       `yaml:"atr_lookback_days_crypto"`
	Bucketing                     string    `yaml:"bucketing"` // "per_symbol" only in phase 1
	TercileLowPctile              float64   `yaml:"tercile_low_pctile"`
	TercileHighPctile             float64   `yaml:"tercile_high_pctile"`
	TercileMultipliers            []float64 `yaml:"tercile_multipliers"`
	InsufficientHistoryMultiplier float64   `yaml:"insufficient_history_multiplier"`
	MinHistoryDays                int       `yaml:"min_history_days"`
}

const (
	defaultDBPort          = 5432
	defaultDBSSLMode       = "disable"
	defaultDBMaxPoolSize   = 100
	defaultServerPort      = 8080
	defaultLogLevel        = "info"
	defaultDataURL         = "https://data.alpaca.markets"
	defaultFeed            = "iex"
	defaultMaxRiskPct      = 2.0
	defaultSlippageBPS     = 10
	defaultKillMaxStops    = 3
	defaultKillWindow      = "2m"
	defaultKillHalt        = "15m"
	defaultMaxDailyLossPct = 5.0  // 5% of account equity
	defaultMaxDailyLossUSD = 5000 // absolute USD cap
	defaultAIBaseURL       = "https://openrouter.ai/api"
	defaultAIMinConfidence = 0.6
	defaultCryptoDataURL   = "wss://stream.data.alpaca.markets"
	defaultCryptoFeed      = "us-1"

	defaultOptionsMinOI     = 10
	defaultOptionsMaxSpread = 0.15
	defaultOptionsMaxIV     = 1.0
	defaultOptionsMinDTE    = 7
)

// Load loads the configuration from env and yaml files.
// The loading sequence is: .env → YAML → env overlay → defaults → validate
func Load(envPath, yamlPath string) (*Config, error) {
	// 1. Parse .env file
	if err := loadEnvFile(envPath); err != nil {
		return nil, fmt.Errorf("failed to load env file: %w", err)
	}

	// 2. Read and parse YAML file
	yamlBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file: %w", err)
	}

	// Apply defaults
	raw := rawConfig{
		Alpaca: AlpacaConfig{
			PaperMode:     true,
			DataURL:       defaultDataURL,
			Feed:          defaultFeed,
			CryptoDataURL: defaultCryptoDataURL,
			CryptoFeed:    defaultCryptoFeed,
		},
		Coinbase: CoinbaseConfig{
			BaseURL:        "https://api.exchange.coinbase.com",
			RateLimitRPS:   8,
			TimeoutSeconds: 30,
		},
		IBKR: IBKRConfig{
			Host:      "localhost",
			Port:      4002,
			ClientID:  1,
			PaperMode: true,
		},
		Database: DatabaseConfig{
			Port:        defaultDBPort,
			SSLMode:     defaultDBSSLMode,
			MaxPoolSize: defaultDBMaxPoolSize,
		},
		Trading: rawTradingConfig{
			MaxRiskPercent:         defaultMaxRiskPct,
			DefaultSlippageBPS:     defaultSlippageBPS,
			KillSwitchMaxStops:     defaultKillMaxStops,
			KillSwitchWindow:       defaultKillWindow,
			KillSwitchHaltDuration: defaultKillHalt,
			MaxDailyLossPct:        defaultMaxDailyLossPct,
			MaxDailyLossUSD:        defaultMaxDailyLossUSD,
			OptionsRisk: OptionsRiskConfig{
				MinOpenInterest: defaultOptionsMinOI,
				MaxSpreadPct:    defaultOptionsMaxSpread,
				MaxIVCeiling:    defaultOptionsMaxIV,
				MinDTE:          defaultOptionsMinDTE,
			},
		},
		Server: ServerConfig{
			Port:     defaultServerPort,
			LogLevel: defaultLogLevel,
		},
		AI: AIConfig{
			BaseURL:       defaultAIBaseURL,
			MinConfidence: defaultAIMinConfidence,
			Enabled:       false,
		},
		AIScreener: AIScreenerConfig{
			Enabled: true,
		},
	}

	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse YAML file: %w", err)
	}
	raw.Symbols.Normalize()

	var killSwitchWindow time.Duration
	if raw.Trading.KillSwitchWindow != "" {
		parsed, err := time.ParseDuration(raw.Trading.KillSwitchWindow)
		if err != nil {
			return nil, fmt.Errorf("invalid kill_switch_window: %w", err)
		}
		killSwitchWindow = parsed
	}

	var killSwitchHalt time.Duration
	if raw.Trading.KillSwitchHaltDuration != "" {
		parsed, err := time.ParseDuration(raw.Trading.KillSwitchHaltDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid kill_switch_halt_duration: %w", err)
		}
		killSwitchHalt = parsed
	}

	cfg := &Config{
		Alpaca:      raw.Alpaca,
		Coinbase:    applyCoinbaseDefaults(raw.Coinbase),
		IBKR:        raw.IBKR,
		Hyperliquid: applyHyperliquidDefaults(raw.Hyperliquid),
		Bybit:       applyBybitDefaults(raw.Bybit),
		Database:    raw.Database,
		Trading: TradingConfig{
			MaxRiskPercent:         raw.Trading.MaxRiskPercent,
			DefaultSlippageBPS:     raw.Trading.DefaultSlippageBPS,
			KillSwitchMaxStops:     raw.Trading.KillSwitchMaxStops,
			KillSwitchWindow:       killSwitchWindow,
			KillSwitchHaltDuration: killSwitchHalt,
			MaxDailyLossPct:        raw.Trading.MaxDailyLossPct,
			MaxDailyLossUSD:        raw.Trading.MaxDailyLossUSD,
			MaxSimultaneousPos:     raw.Trading.MaxSimultaneousPos,
			MaxPositionsPerGroup:   raw.Trading.MaxPositionsPerGroup,
			MaxPortfolioHeat:       raw.Trading.MaxPortfolioHeat,
			MaxSectorExposure:      raw.Trading.MaxSectorExposure,
			MaxIndustryExposure:    raw.Trading.MaxIndustryExposure,
			MaxDirectionalBias:     raw.Trading.MaxDirectionalBias,
			SymbolMetadataPath:     raw.Trading.SymbolMetadataPath,
			OptionsRisk:            raw.Trading.OptionsRisk,
			PDTEnforcement:         raw.Trading.PDTEnforcement,
			RegTEnforcement:        raw.Trading.RegTEnforcement,
			EarningsBlackout:       raw.Trading.EarningsBlackout,
			MacroEventBlackoutMinutes: raw.Trading.MacroEventBlackoutMinutes,
			MacroEventImpacts:      raw.Trading.MacroEventImpacts,
		},
		Symbols:      raw.Symbols,
		Server:       raw.Server,
		AI:           raw.AI,
		AIScreener:   applyAIScreenerDefaults(raw.AIScreener),
		Notification: raw.Notification,
		Backtest:     applyBacktestDefaults(raw.Backtest),
		Options:      applyOptionsDefaults(raw.Options),
		Deribit:      applyDeribitDefaults(raw.Deribit),
		OnChain:      applyOnChainDefaults(raw.OnChain),
		Risk:         applyRiskDefaults(raw.Risk),
		Exits:        applyExitsDefaults(raw.Exits),
	}

	// 3. Overlay environment variables
	if val := os.Getenv("APCA_API_KEY_ID"); val != "" {
		cfg.Alpaca.APIKeyID = val
	}
	if val := os.Getenv("APCA_API_SECRET_KEY"); val != "" {
		cfg.Alpaca.APISecretKey = val
	}
	if val := os.Getenv("APCA_DATA_FEED"); val != "" {
		cfg.Alpaca.Feed = val
	}
	if val := os.Getenv("APCA_API_BASE_URL"); val != "" {
		cfg.Alpaca.BaseURL = val
	}
	if val := os.Getenv("APCA_DATA_URL"); val != "" {
		cfg.Alpaca.DataURL = val
	}
	if val := os.Getenv("APCA_CRYPTO_DATA_URL"); val != "" {
		cfg.Alpaca.CryptoDataURL = val
	}
	if val := os.Getenv("APCA_CRYPTO_FEED"); val != "" {
		cfg.Alpaca.CryptoFeed = val
	}

	if val := os.Getenv("STREAMING_SOURCE"); val != "" {
		cfg.StreamingSource = val
	} else {
		cfg.StreamingSource = "alpaca"
	}
	if val := os.Getenv("IBKR_GATEWAY_HOST"); val != "" {
		cfg.IBKR.Host = val
	}
	if val := os.Getenv("IBKR_GATEWAY_PORT"); val != "" {
		if p, err := strconv.Atoi(val); err == nil {
			cfg.IBKR.Port = p
		}
	}
	if val := os.Getenv("IBKR_CLIENT_ID"); val != "" {
		if id, err := strconv.Atoi(val); err == nil {
			cfg.IBKR.ClientID = id
		}
	}
	if val := os.Getenv("IBKR_ACCOUNT_ID"); val != "" {
		cfg.IBKR.AccountID = val
	}
	if val := os.Getenv("IBKR_MARKET_DATA_TYPE"); val != "" {
		if t, err := strconv.Atoi(val); err == nil {
			cfg.IBKR.MarketDataType = t
		}
	}

	if val := os.Getenv("TIMESCALEDB_PASSWORD"); val != "" {
		cfg.Database.Password = val
	}
	if val := os.Getenv("TIMESCALEDB_HOST"); val != "" {
		cfg.Database.Host = val
	}
	if val := os.Getenv("TIMESCALEDB_PORT"); val != "" {
		if p, err := strconv.Atoi(val); err == nil {
			cfg.Database.Port = p
		}
	}
	if val := os.Getenv("LLM_BASE_URL"); val != "" {
		cfg.AI.BaseURL = val
	}
	if val := os.Getenv("LLM_MODEL"); val != "" {
		cfg.AI.Model = val
	}
	if val := os.Getenv("LLM_BACKTEST_MODEL"); val != "" {
		cfg.AI.BacktestModel = val
	}
	if val := os.Getenv("LLM_API_KEY"); val != "" {
		cfg.AI.APIKey = val
	}
	if val := os.Getenv("LLM_MIN_CONFIDENCE"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			cfg.AI.MinConfidence = f
		}
	}
	if val := os.Getenv("LLM_ENABLED"); val == "true" {
		cfg.AI.Enabled = true
	}
	if val := os.Getenv("LLM_PROVIDER_SORT"); val != "" {
		cfg.AI.ProviderSort = val
	}
	if val := os.Getenv("TELEGRAM_BOT_TOKEN"); val != "" {
		cfg.Notification.TelegramBotToken = val
	}
	if val := os.Getenv("TELEGRAM_CHAT_ID"); val != "" {
		cfg.Notification.TelegramChatID = val
	}
	if val := os.Getenv("DISCORD_WEBHOOK_URL"); val != "" {
		cfg.Notification.DiscordWebhookURL = val
	}
	if val := os.Getenv("KAKAO_REST_API_KEY"); val != "" {
		cfg.Notification.KakaoRestAPIKey = val
	}
	if val := os.Getenv("KAKAO_REDIRECT_URI"); val != "" {
		cfg.Notification.KakaoRedirectURI = val
	}
	// Hyperliquid env overlays
	if val := os.Getenv("HYPERLIQUID_PRIVATE_KEY"); val != "" {
		cfg.Hyperliquid.PrivateKey = val
	}
	if val := os.Getenv("HYPERLIQUID_ADDRESS"); val != "" {
		cfg.Hyperliquid.Address = val
	}
	if val := os.Getenv("HYPERLIQUID_NETWORK"); val != "" {
		cfg.Hyperliquid.Network = val
	}
	if val := os.Getenv("HYPERLIQUID_VAULT_ADDRESS"); val != "" {
		cfg.Hyperliquid.VaultAddress = val
	}

	// On-chain / Dune env overlays
	if val := os.Getenv("DUNE_API_KEY"); val != "" {
		cfg.OnChain.DuneAPIKey = val
	}

	if val := os.Getenv("AI_SCREENER_ENABLED"); val != "" {
		cfg.AIScreener.Enabled = val == "true"
	}
	if val := os.Getenv("OPTIONS_V2"); val == "true" {
		cfg.OptionsV2 = true
	}
	if val := os.Getenv("MULTI_ACCOUNT"); val == "true" {
		cfg.MultiAccount = true
	}
	if val := os.Getenv("OMO_ORDER_JOURNAL_ENABLED"); val == "true" {
		cfg.OrderJournalEnabled = true
	}

	// Validate configuration
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	return cfg, nil
}

// loadEnvFile parses a .env file and sets environment variables.
// It skips missing files, and existing environment variables take precedence.
func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Skip if the file does not exist
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if _, exists := os.LookupEnv(key); !exists {
				_ = os.Setenv(key, val)
			}
		}
	}
	return scanner.Err()
}

// applyBacktestDefaults fills in zero-valued Sprint-7 fields. Per the Sprint-7
// plan the runtime default is "realistic" so the §3 AVWAP same-bar look-ahead
// bug is patched out of the box; operators can still opt back into
// "optimistic" via YAML for legacy parity runs.
func applyBacktestDefaults(c BacktestConfig) BacktestConfig {
	if c.FillModel == "" {
		c.FillModel = "realistic"
	}
	if c.LatencyMsEquity == 0 {
		c.LatencyMsEquity = 50
	}
	if c.LatencyMsOption == 0 {
		c.LatencyMsOption = 200
	}
	if c.FeeSchedule == "" {
		c.FeeSchedule = "none"
	}
	if c.PessimisticSlippageMultiplier == 0 {
		c.PessimisticSlippageMultiplier = 2.0
	}
	return c
}

// applyOptionsDefaults fills in the Theta Data plumbing defaults. The
// disabled-by-default UseLiveMarketData stays false unless the YAML
// explicitly enables it, so behavior is unchanged on every existing
// deploy.
func applyOptionsDefaults(c OptionsConfig) OptionsConfig {
	if c.ThetaData.BaseURL == "" {
		c.ThetaData.BaseURL = "https://rest.thetadata.net"
	}
	if c.ThetaData.RateLimitPerSec <= 0 {
		c.ThetaData.RateLimitPerSec = 10
	}
	return c
}

// applyDeribitDefaults fills in sensible defaults for the Deribit IV surface
// adapter. The adapter is effectively disabled when Assets is empty — the
// default populates BTC and ETH so the skew-regime classifier has data
// immediately on first enable.
func applyDeribitDefaults(c DeribitConfig) DeribitConfig {
	if c.BaseURL == "" {
		c.BaseURL = "https://www.deribit.com/api/v2/"
	}
	if c.PollInterval == "" {
		c.PollInterval = "5m"
	}
	if len(c.Assets) == 0 {
		c.Assets = []string{"BTC", "ETH"}
	}
	return c
}

func applyAIScreenerDefaults(c AIScreenerConfig) AIScreenerConfig {
	if len(c.Models) == 0 {
		c.Models = []string{
			"google/gemini-2.5-flash-lite",
			"deepseek/deepseek-chat-v3",
			"anthropic/claude-3.5-haiku",
		}
	}
	if c.NumericRunAtHourET == 0 {
		c.NumericRunAtHourET = 8
	}
	if c.AIRunAtHourET == 0 {
		c.AIRunAtHourET = 8
	}
	if c.AIRunAtMinuteET == 0 {
		c.AIRunAtMinuteET = 35
	}
	if c.Pass0MinPrice == 0 {
		c.Pass0MinPrice = 10.0
	}
	if c.Pass0MinVolume == 0 {
		c.Pass0MinVolume = 50000
	}
	if c.Pass0MinADV == 0 {
		c.Pass0MinADV = 500_000
	}
	if c.MaxCandidatesPerCall == 0 {
		c.MaxCandidatesPerCall = 20
	}
	if c.TopNPerStrategy == 0 {
		c.TopNPerStrategy = 10
	}
	return c
}

// applyOnChainDefaults fills in the on-chain flow adapter defaults. The
// adapter is disabled by default; operators must set enabled: true and
// provide a Dune API key in YAML to activate.
func applyOnChainDefaults(c OnChainConfig) OnChainConfig {
	if c.DuneBaseURL == "" {
		c.DuneBaseURL = "https://api.dune.com/api/v1/"
	}
	if c.CacheTTL == "" {
		c.CacheTTL = "5m"
	}
	return c
}

// applyRiskDefaults materializes PositionRiskCapConfig defaults. Quant
// ships this ENABLED with textbook-safe numbers (25 bps daily budget,
// 20% per trade, widest-active stop source). Operators flip Enabled=false
// to kill-switch it. When a YAML section exists, only blank/zero fields
// fall back to defaults so overrides stay surgical.
func applyRiskDefaults(c RiskConfig) RiskConfig {
	p := c.PositionCap
	// Default-on per quant. If YAML omits the section, zero-value Enabled
	// is false — we must bake the default-on here by presence detection.
	// We treat "all numeric knobs zero + AppliesTo empty" as "omitted".
	omitted := p.Mode == "" && p.DailyLossBudgetPct == 0 &&
		p.DailyLossBudgetUSD == 0 && p.MaxPositionRiskFrac == 0 &&
		p.StopPctSource == "" && !p.Enabled && len(p.AppliesTo) == 0
	if omitted {
		p.Enabled = true
	}
	if p.Mode == "" {
		p.Mode = "account_pct"
	}
	if p.DailyLossBudgetPct == 0 {
		p.DailyLossBudgetPct = 0.0025
	}
	if p.DailyLossBudgetUSD == 0 {
		p.DailyLossBudgetUSD = 2500
	}
	if p.MaxPositionRiskFrac == 0 {
		p.MaxPositionRiskFrac = 0.20
	}
	if p.StopPctSource == "" {
		p.StopPctSource = "widest_active"
	}
	if p.ConfluenceBonusMax == 0 {
		p.ConfluenceBonusMax = 1.0
	}
	if len(p.AppliesTo) == 0 {
		p.AppliesTo = []string{"options"}
	}
	// RejectOnFloor: default true when section omitted (enabled path).
	if omitted {
		p.RejectOnFloor = true
	}
	c.PositionCap = p
	return c
}

// applyExitsDefaults materializes ATRTrailConfig defaults. Same
// presence-detection idiom as applyRiskDefaults so Enabled defaults to
// true when the YAML section is omitted but operators can still
// explicitly set enabled: false to kill the scaler.
func applyExitsDefaults(c ExitsConfig) ExitsConfig {
	a := c.ATRTrail
	omitted := !a.Enabled && a.ATRPeriod == 0 && a.ATRTimeframe == "" &&
		a.ATRLookbackDays == 0 && a.ATRLookbackDaysCrypto == 0 &&
		a.Bucketing == "" && a.TercileLowPctile == 0 && a.TercileHighPctile == 0 &&
		len(a.TercileMultipliers) == 0 && a.InsufficientHistoryMultiplier == 0 &&
		a.MinHistoryDays == 0
	if omitted {
		a.Enabled = true
	}
	if a.ATRPeriod == 0 {
		a.ATRPeriod = 14
	}
	if a.ATRTimeframe == "" {
		a.ATRTimeframe = "1d"
	}
	if a.ATRLookbackDays == 0 {
		a.ATRLookbackDays = 60
	}
	if a.ATRLookbackDaysCrypto == 0 {
		a.ATRLookbackDaysCrypto = 42
	}
	if a.Bucketing == "" {
		a.Bucketing = "per_symbol"
	}
	if a.TercileLowPctile == 0 {
		a.TercileLowPctile = 0.33
	}
	if a.TercileHighPctile == 0 {
		a.TercileHighPctile = 0.67
	}
	if len(a.TercileMultipliers) == 0 {
		a.TercileMultipliers = []float64{1.0, 1.5, 2.0}
	}
	if a.InsufficientHistoryMultiplier == 0 {
		a.InsufficientHistoryMultiplier = 1.0
	}
	if a.MinHistoryDays == 0 {
		a.MinHistoryDays = 30
	}
	c.ATRTrail = a
	return c
}

// applyBybitDefaults fills in the Bybit adapter defaults. The adapter is
// disabled by default; operators must set enabled: true in YAML to activate.
func applyBybitDefaults(c BybitConfig) BybitConfig {
	if c.BaseURL == "" {
		c.BaseURL = "https://api.bybit.com"
	}
	return c
}

// applyCoinbaseDefaults fills in the Coinbase Exchange public market-data
// adapter defaults. Always safe to call — the adapter is read-only and needs
// no credentials, so omitted YAML sections still produce a usable client.
func applyCoinbaseDefaults(c CoinbaseConfig) CoinbaseConfig {
	if c.BaseURL == "" {
		c.BaseURL = "https://api.exchange.coinbase.com"
	}
	if c.RateLimitRPS <= 0 {
		c.RateLimitRPS = 8
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 30
	}
	return c
}

// applyHyperliquidDefaults fills in safe defaults for HyperliquidConfig.
// Network defaults to "testnet" so accidental mainnet orders are impossible.
func applyHyperliquidDefaults(c HyperliquidConfig) HyperliquidConfig {
	if c.Network == "" {
		c.Network = "testnet"
	}
	return c
}

// validate checks if the given configuration is valid.
func validate(cfg *Config) error {
	if cfg.Trading.MaxRiskPercent < 0 {
		return fmt.Errorf("config validation: maxRiskPercent cannot be negative")
	}
	if len(cfg.Symbols.Groups) == 0 {
		return fmt.Errorf("config validation: symbols groups cannot be empty")
	}
	validTimeframes := map[string]bool{
		"1m": true, "5m": true, "15m": true, "1h": true, "1d": true,
	}
	for _, g := range cfg.Symbols.Groups {
		if g.AssetClass != "EQUITY" && g.AssetClass != "CRYPTO" {
			return fmt.Errorf("config validation: invalid asset class %q", g.AssetClass)
		}
		if len(g.Symbols) == 0 {
			return fmt.Errorf("config validation: symbol group %q has no symbols", g.AssetClass)
		}
		if !validTimeframes[g.Timeframe] {
			return fmt.Errorf("config validation: invalid timeframe %q for group %q", g.Timeframe, g.AssetClass)
		}
	}
	if cfg.Database.Host == "" {
		return fmt.Errorf("config validation: database host cannot be empty")
	}
	// Alpaca credentials are always required (market data source).
	if cfg.Alpaca.APIKeyID == "" {
		return fmt.Errorf("config validation: alpaca API key ID cannot be empty (required for market data)")
	}
	if cfg.Alpaca.APISecretKey == "" {
		return fmt.Errorf("config validation: alpaca API secret key cannot be empty (required for market data)")
	}
	if cfg.Alpaca.BaseURL == "" {
		return fmt.Errorf("config validation: alpaca base URL cannot be empty")
	}
	// IBKR is the only execution broker -- host and port are required.
	if cfg.IBKR.Host == "" {
		return fmt.Errorf("config validation: IBKR host cannot be empty")
	}
	if cfg.IBKR.Port == 0 {
		return fmt.Errorf("config validation: IBKR port cannot be zero")
	}
	return nil
}
