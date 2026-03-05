package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create temporary files for testing
func writeFile(t *testing.T, dir, name, content string) string {
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
	return path
}

func TestLoad_ValidConfig(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()

	envContent := `APCA_API_KEY_ID=test-key-123
APCA_API_SECRET_KEY=test-secret-456
TIMESCALEDB_PASSWORD=test-db-pass`
	envPath := writeFile(t, tempDir, ".env", envContent)

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
  data_url: https://data.alpaca.markets
  paper_mode: true

database:
  host: localhost
  port: 5432
  user: opentrade
  dbname: opentrade
  ssl_mode: disable
  max_pool_size: 10

trading:
  max_risk_percent: 2.0
  default_slippage_bps: 10
  kill_switch_max_stops: 3
  kill_switch_window: 2m
  kill_switch_halt_duration: 15m

symbols:
  symbols:
    - AAPL
    - MSFT
    - GOOGL
  timeframe: 1m

server:
  port: 8080
  log_level: info`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	cfg, err := Load(envPath, yamlPath)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Check AlpacaConfig
	assert.Equal(t, "test-key-123", cfg.Alpaca.APIKeyID)
	assert.Equal(t, "test-secret-456", cfg.Alpaca.APISecretKey)
	assert.Equal(t, "https://paper-api.alpaca.markets", cfg.Alpaca.BaseURL)
	assert.Equal(t, "https://data.alpaca.markets", cfg.Alpaca.DataURL)
	assert.True(t, cfg.Alpaca.PaperMode)

	// Check DatabaseConfig
	assert.Equal(t, "localhost", cfg.Database.Host)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "opentrade", cfg.Database.User)
	assert.Equal(t, "test-db-pass", cfg.Database.Password)
	assert.Equal(t, "opentrade", cfg.Database.DBName)
	assert.Equal(t, "disable", cfg.Database.SSLMode)
	assert.Equal(t, 10, cfg.Database.MaxPoolSize)

	// Check TradingConfig
	assert.Equal(t, 2.0, cfg.Trading.MaxRiskPercent)
	assert.Equal(t, 10, cfg.Trading.DefaultSlippageBPS)
	assert.Equal(t, 3, cfg.Trading.KillSwitchMaxStops)
	assert.Equal(t, 2*time.Minute, cfg.Trading.KillSwitchWindow)
	assert.Equal(t, 15*time.Minute, cfg.Trading.KillSwitchHaltDuration)

	// Check SymbolsConfig
	assert.Equal(t, []string{"AAPL", "MSFT", "GOOGL"}, cfg.Symbols.Symbols)
	assert.Equal(t, "1m", cfg.Symbols.Timeframe)

	// Check ServerConfig
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, "info", cfg.Server.LogLevel)
}

func TestLoad_DefaultValues(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()

	envContent := `APCA_API_KEY_ID=test-key
APCA_API_SECRET_KEY=test-secret`
	envPath := writeFile(t, tempDir, ".env", envContent)

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
  user: opentrade
  dbname: opentrade
symbols:
  symbols:
    - AAPL
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	cfg, err := Load(envPath, yamlPath)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Check defaults
	assert.Equal(t, 2.0, cfg.Trading.MaxRiskPercent)
	assert.Equal(t, 10, cfg.Trading.DefaultSlippageBPS)
	assert.Equal(t, 3, cfg.Trading.KillSwitchMaxStops)
	assert.Equal(t, 2*time.Minute, cfg.Trading.KillSwitchWindow)
	assert.Equal(t, 15*time.Minute, cfg.Trading.KillSwitchHaltDuration)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "disable", cfg.Database.SSLMode)
	assert.Equal(t, 10, cfg.Database.MaxPoolSize)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, "info", cfg.Server.LogLevel)
	assert.Equal(t, true, cfg.Alpaca.PaperMode)
	assert.Equal(t, "https://data.alpaca.markets", cfg.Alpaca.DataURL)
}

func TestLoad_EnvOverridesSecrets(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()

	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=from-env-file")

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
  user: opentrade
  dbname: opentrade
symbols:
  symbols:
    - AAPL
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	t.Setenv("APCA_API_KEY_ID", "env-override-key")
	t.Setenv("APCA_API_SECRET_KEY", "env-override-secret")
	t.Setenv("TIMESCALEDB_PASSWORD", "env-override-db")

	// Act
	cfg, err := Load(envPath, yamlPath)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "env-override-key", cfg.Alpaca.APIKeyID)
	assert.Equal(t, "env-override-secret", cfg.Alpaca.APISecretKey)
	assert.Equal(t, "env-override-db", cfg.Database.Password)
}

func TestLoad_MissingRequiredEnvVar(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env") // Does not exist

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
symbols:
  symbols: [AAPL]
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Ensure env vars are empty
	os.Unsetenv("APCA_API_KEY_ID")
	os.Unsetenv("APCA_API_SECRET_KEY")

	// Act
	_, err := Load(envPath, yamlPath)

	// Assert
	require.Error(t, err)
}

func TestLoad_MissingYAMLFile(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envContent := `APCA_API_KEY_ID=test
APCA_API_SECRET_KEY=test`
	envPath := writeFile(t, tempDir, ".env", envContent)
	yamlPath := filepath.Join(tempDir, "missing.yaml")

	// Act
	_, err := Load(envPath, yamlPath)

	// Assert
	require.Error(t, err)
}

func TestLoad_InvalidYAML(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envContent := `APCA_API_KEY_ID=test
APCA_API_SECRET_KEY=test`
	envPath := writeFile(t, tempDir, ".env", envContent)

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
  - invalid_yaml_syntax: [
database:
  host: localhost`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	_, err := Load(envPath, yamlPath)

	// Assert
	require.Error(t, err)
}

func TestLoad_ValidationError_NegativeRisk(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=a\nAPCA_API_SECRET_KEY=b")

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
trading:
  max_risk_percent: -1.0
symbols:
  symbols: [AAPL]
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	_, err := Load(envPath, yamlPath)

	// Assert
	require.Error(t, err)
}

func TestLoad_ValidationError_ZeroSymbols(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=a\nAPCA_API_SECRET_KEY=b")

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
symbols:
  symbols: []
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	_, err := Load(envPath, yamlPath)

	// Assert
	require.Error(t, err)
}

func TestLoad_ValidationError_InvalidTimeframe(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=a\nAPCA_API_SECRET_KEY=b")

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
symbols:
  symbols: [AAPL]
  timeframe: 2m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	_, err := Load(envPath, yamlPath)

	// Assert
	require.Error(t, err)
}

func TestLoad_ValidationError_EmptyDBHost(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=a\nAPCA_API_SECRET_KEY=b")

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: ""
symbols:
  symbols: [AAPL]
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	_, err := Load(envPath, yamlPath)

	// Assert
	require.Error(t, err)
}

func TestConfig_OptionsV2_Default(t *testing.T) {
	tempDir := t.TempDir()

	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=k\nAPCA_API_SECRET_KEY=s")
	yamlPath := writeFile(t, tempDir, "config.yaml", `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
  user: opentrade
  dbname: opentrade
symbols:
  symbols: [AAPL]
  timeframe: 1m`)

	cfg, err := Load(envPath, yamlPath)
	require.NoError(t, err)
	assert.False(t, cfg.OptionsV2)
}

func TestConfig_OptionsV2_Enabled(t *testing.T) {
	tempDir := t.TempDir()

	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=k\nAPCA_API_SECRET_KEY=s")
	yamlPath := writeFile(t, tempDir, "config.yaml", `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
  user: opentrade
  dbname: opentrade
symbols:
  symbols: [AAPL]
  timeframe: 1m`)

	t.Setenv("OPTIONS_V2", "true")

	cfg, err := Load(envPath, yamlPath)
	require.NoError(t, err)
	assert.True(t, cfg.OptionsV2)
}

func TestConfig_MultiAccount_Default(t *testing.T) {
	tempDir := t.TempDir()

	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=k\nAPCA_API_SECRET_KEY=s")
	yamlPath := writeFile(t, tempDir, "config.yaml", `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
  user: opentrade
  dbname: opentrade
symbols:
  symbols: [AAPL]
  timeframe: 1m`)

	cfg, err := Load(envPath, yamlPath)
	require.NoError(t, err)
	assert.False(t, cfg.MultiAccount)
}

func TestConfig_MultiAccount_Enabled(t *testing.T) {
	tempDir := t.TempDir()

	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=k\nAPCA_API_SECRET_KEY=s")
	yamlPath := writeFile(t, tempDir, "config.yaml", `alpaca:
  base_url: https://paper-api.alpaca.markets
database:
  host: localhost
  user: opentrade
  dbname: opentrade
symbols:
  symbols: [AAPL]
  timeframe: 1m`)

	t.Setenv("MULTI_ACCOUNT", "true")

	cfg, err := Load(envPath, yamlPath)
	require.NoError(t, err)
	assert.True(t, cfg.MultiAccount)
}

func TestLoad_EnvOverridesAlpacaBaseURL(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=k\nAPCA_API_SECRET_KEY=s")

	yamlContent := `alpaca:
  base_url: https://yaml-url.example.com
database:
  host: localhost
symbols:
  symbols: [AAPL]
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	t.Setenv("APCA_API_KEY_ID", "k")
	t.Setenv("APCA_API_SECRET_KEY", "s")
	t.Setenv("APCA_API_BASE_URL", "https://paper-api.alpaca.markets")

	// Act
	cfg, err := Load(envPath, yamlPath)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "https://paper-api.alpaca.markets", cfg.Alpaca.BaseURL)
}

func TestLoad_EnvOverridesAlpacaDataURL(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	envPath := writeFile(t, tempDir, ".env", "APCA_API_KEY_ID=k\nAPCA_API_SECRET_KEY=s")

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
  data_url: https://yaml-data.example.com
database:
  host: localhost
symbols:
  symbols: [AAPL]
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	t.Setenv("APCA_API_KEY_ID", "k")
	t.Setenv("APCA_API_SECRET_KEY", "s")
	t.Setenv("APCA_DATA_URL", "https://custom-data.alpaca.markets")

	// Act
	cfg, err := Load(envPath, yamlPath)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "https://custom-data.alpaca.markets", cfg.Alpaca.DataURL)
}

func TestLoad_EnvOverridesAlpacaBaseURL_FromEnvFile(t *testing.T) {
	// Arrange — APCA_API_BASE_URL set via .env file (not os env), should be overlaid
	tempDir := t.TempDir()
	envContent := `APCA_API_KEY_ID=k
APCA_API_SECRET_KEY=s
APCA_API_BASE_URL=https://paper-api.alpaca.markets`
	envPath := writeFile(t, tempDir, ".env", envContent)

	yamlContent := `alpaca:
  base_url: https://yaml-url.example.com
database:
  host: localhost
symbols:
  symbols: [AAPL]
  timeframe: 1m`
	yamlPath := writeFile(t, tempDir, "config.yaml", yamlContent)

	// Act
	cfg, err := Load(envPath, yamlPath)

	// Assert — env file value should override yaml value
	require.NoError(t, err)
	assert.Equal(t, "https://paper-api.alpaca.markets", cfg.Alpaca.BaseURL)
}
