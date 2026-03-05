package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// AccountConfig represents a single trading account's configuration.
// Credentials are resolved from environment variables via EnvKeyID/EnvSecretKey
// to avoid storing secrets in config files.
type AccountConfig struct {
	Label        string `toml:"label"`
	TenantID     string `toml:"tenant_id"`
	EnvKeyID     string `toml:"env_key_id"`
	EnvSecretKey string `toml:"env_secret_key"`
	BaseURL      string `toml:"base_url"`
	PaperMode    bool   `toml:"paper_mode"`
}

// ResolvedAPIKeyID returns the API key ID from the environment variable
// specified by EnvKeyID. Returns empty string if not set.
func (a AccountConfig) ResolvedAPIKeyID() string {
	return os.Getenv(a.EnvKeyID)
}

// ResolvedAPISecretKey returns the API secret key from the environment variable
// specified by EnvSecretKey. Returns empty string if not set.
func (a AccountConfig) ResolvedAPISecretKey() string {
	return os.Getenv(a.EnvSecretKey)
}

// ToAlpacaConfig converts an AccountConfig to an AlpacaConfig by resolving
// environment variables for credentials.
func (a AccountConfig) ToAlpacaConfig() AlpacaConfig {
	return AlpacaConfig{
		APIKeyID:     a.ResolvedAPIKeyID(),
		APISecretKey: a.ResolvedAPISecretKey(),
		BaseURL:      a.BaseURL,
		PaperMode:    a.PaperMode,
		DataURL:      "https://data.alpaca.markets",
		Feed:         "iex",
	}
}

// accountsFile is the TOML structure for configs/accounts.toml.
type accountsFile struct {
	Accounts []AccountConfig `toml:"accounts"`
}

// LoadAccounts reads a multi-account TOML config file and returns validated
// account configurations. Returns an error if the file is unreadable, has
// no accounts, or any account fails validation.
func LoadAccounts(path string) ([]AccountConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read accounts config: %w", err)
	}

	var file accountsFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("failed to parse accounts config: %w", err)
	}

	if len(file.Accounts) == 0 {
		return nil, fmt.Errorf("accounts config: no accounts defined")
	}

	seen := make(map[string]bool, len(file.Accounts))
	for i, acct := range file.Accounts {
		if err := validateAccount(acct, i); err != nil {
			return nil, err
		}
		if seen[acct.TenantID] {
			return nil, fmt.Errorf("accounts config: duplicate tenant_id %q", acct.TenantID)
		}
		seen[acct.TenantID] = true
	}

	return file.Accounts, nil
}

// validateAccount checks that a single account configuration has all required fields.
func validateAccount(acct AccountConfig, index int) error {
	if acct.Label == "" {
		return fmt.Errorf("accounts config[%d]: label is required", index)
	}
	if acct.TenantID == "" {
		return fmt.Errorf("accounts config[%d]: tenant_id is required", index)
	}
	if acct.EnvKeyID == "" {
		return fmt.Errorf("accounts config[%d]: env_key_id is required", index)
	}
	if acct.EnvSecretKey == "" {
		return fmt.Errorf("accounts config[%d]: env_secret_key is required", index)
	}
	if acct.BaseURL == "" {
		return fmt.Errorf("accounts config[%d]: base_url is required", index)
	}
	return nil
}
