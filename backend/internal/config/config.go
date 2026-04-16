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
	IBKR         IBKRConfig         `yaml:"ibkr"`
	StreamingSource string          `yaml:"-"` // "alpaca" or "ibkr"; defaults to "alpaca"
	Database     DatabaseConfig     `yaml:"database"`
	Trading      TradingConfig      `yaml:"trading"`
	Symbols      SymbolsConfig      `yaml:"symbols"`
	Server       ServerConfig       `yaml:"server"`
	AI           AIConfig           `yaml:"ai"`
	AIScreener   AIScreenerConfig   `yaml:"ai_screener"`
	Notification NotificationConfig `yaml:"notification"`
	Backtest     BacktestConfig     `yaml:"backtest"`
	OptionsV2    bool               `yaml:"-"`
	MultiAccount bool               `yaml:"-"`
	// OrderJournalEnabled toggles the Sprint 2 write-ahead order-intent
	// journal and journal-aware startup reconciliation. Default off (legacy
	// behavior — cancel-all on startup, no intent persistence) so production
	// deploys can ship the code and flip the flag independently.
	OrderJournalEnabled bool `yaml:"-"`
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

// AIConfig holds configuration for the AI adversarial debate system.
type AIConfig struct {
	BaseURL       string  `yaml:"base_url"`
	Model         string  `yaml:"model"`
	APIKey        string  `yaml:"api_key"`
	MinConfidence float64 `yaml:"min_confidence"`
	Enabled       bool    `yaml:"enabled"`
	ProviderSort  string  `yaml:"provider_sort"` // OpenRouter provider routing sort (e.g. "latency")
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
	IBKR         IBKRConfig         `yaml:"ibkr"`
	Database     DatabaseConfig     `yaml:"database"`
	Trading      rawTradingConfig   `yaml:"trading"`
	Symbols      SymbolsConfig      `yaml:"symbols"`
	Server       ServerConfig       `yaml:"server"`
	AI           AIConfig           `yaml:"ai"`
	AIScreener   AIScreenerConfig   `yaml:"ai_screener"`
	Notification NotificationConfig `yaml:"notification"`
	Backtest     BacktestConfig     `yaml:"backtest"`
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
		Alpaca:   raw.Alpaca,
		IBKR:     raw.IBKR,
		Database: raw.Database,
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
