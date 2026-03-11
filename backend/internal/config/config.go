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
	Database     DatabaseConfig     `yaml:"database"`
	Trading      TradingConfig      `yaml:"trading"`
	Symbols      SymbolsConfig      `yaml:"symbols"`
	Server       ServerConfig       `yaml:"server"`
	AI           AIConfig           `yaml:"ai"`
	AIScreener   AIScreenerConfig   `yaml:"ai_screener"`
	Notification NotificationConfig `yaml:"notification"`
	OptionsV2    bool               `yaml:"-"`
	MultiAccount bool               `yaml:"-"`
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
	Pass0MinGapPct       float64  `yaml:"pass0_min_gap_pct"`
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
	MaxRiskPercent         float64       `yaml:"max_risk_percent"`
	DefaultSlippageBPS     int           `yaml:"default_slippage_bps"`
	KillSwitchMaxStops     int           `yaml:"kill_switch_max_stops"`
	KillSwitchWindow       time.Duration `yaml:"-"`
	KillSwitchHaltDuration time.Duration `yaml:"-"`
	MaxDailyLossPct        float64       `yaml:"max_daily_loss_pct"`
	MaxDailyLossUSD        float64       `yaml:"max_daily_loss_usd"`
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
	MaxRiskPercent         float64 `yaml:"max_risk_percent"`
	DefaultSlippageBPS     int     `yaml:"default_slippage_bps"`
	KillSwitchMaxStops     int     `yaml:"kill_switch_max_stops"`
	KillSwitchWindow       string  `yaml:"kill_switch_window"`
	KillSwitchHaltDuration string  `yaml:"kill_switch_halt_duration"`
	MaxDailyLossPct        float64 `yaml:"max_daily_loss_pct"`
	MaxDailyLossUSD        float64 `yaml:"max_daily_loss_usd"`
}

type rawConfig struct {
	Alpaca       AlpacaConfig       `yaml:"alpaca"`
	Database     DatabaseConfig     `yaml:"database"`
	Trading      rawTradingConfig   `yaml:"trading"`
	Symbols      SymbolsConfig      `yaml:"symbols"`
	Server       ServerConfig       `yaml:"server"`
	AI           AIConfig           `yaml:"ai"`
	AIScreener   AIScreenerConfig   `yaml:"ai_screener"`
	Notification NotificationConfig `yaml:"notification"`
}

const (
	defaultDBPort          = 5432
	defaultDBSSLMode       = "disable"
	defaultDBMaxPoolSize   = 10
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
		Database: raw.Database,
		Trading: TradingConfig{
			MaxRiskPercent:         raw.Trading.MaxRiskPercent,
			DefaultSlippageBPS:     raw.Trading.DefaultSlippageBPS,
			KillSwitchMaxStops:     raw.Trading.KillSwitchMaxStops,
			KillSwitchWindow:       killSwitchWindow,
			KillSwitchHaltDuration: killSwitchHalt,
			MaxDailyLossPct:        raw.Trading.MaxDailyLossPct,
			MaxDailyLossUSD:        raw.Trading.MaxDailyLossUSD,
		},
		Symbols:      raw.Symbols,
		Server:       raw.Server,
		AI:           raw.AI,
		AIScreener:   applyAIScreenerDefaults(raw.AIScreener),
		Notification: raw.Notification,
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
	if cfg.Alpaca.APIKeyID == "" {
		return fmt.Errorf("config validation: alpaca API key ID cannot be empty")
	}
	if cfg.Alpaca.APISecretKey == "" {
		return fmt.Errorf("config validation: alpaca API secret key cannot be empty")
	}
	if cfg.Alpaca.BaseURL == "" {
		return fmt.Errorf("config validation: alpaca base URL cannot be empty")
	}
	return nil
}
