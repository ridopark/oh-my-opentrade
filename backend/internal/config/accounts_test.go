package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAccounts_ValidConfig(t *testing.T) {
	content := `
[[accounts]]
label      = "primary"
tenant_id  = "primary"
env_key_id     = "TEST_KEY_1"
env_secret_key = "TEST_SECRET_1"
base_url   = "https://paper-api.alpaca.markets"
paper_mode = true

[[accounts]]
label      = "secondary"
tenant_id  = "secondary"
env_key_id     = "TEST_KEY_2"
env_secret_key = "TEST_SECRET_2"
base_url   = "https://paper-api.alpaca.markets"
paper_mode = true
`
	path := writeTempTOML(t, content)
	accounts, err := LoadAccounts(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	if accounts[0].TenantID != "primary" {
		t.Errorf("expected tenant_id 'primary', got %q", accounts[0].TenantID)
	}
	if accounts[1].TenantID != "secondary" {
		t.Errorf("expected tenant_id 'secondary', got %q", accounts[1].TenantID)
	}
	if accounts[0].Label != "primary" {
		t.Errorf("expected label 'primary', got %q", accounts[0].Label)
	}
	if !accounts[0].PaperMode {
		t.Error("expected paper_mode true for primary")
	}
}

func TestLoadAccounts_DuplicateTenantID(t *testing.T) {
	content := `
[[accounts]]
label      = "a"
tenant_id  = "same"
env_key_id     = "K1"
env_secret_key = "S1"
base_url   = "https://paper-api.alpaca.markets"

[[accounts]]
label      = "b"
tenant_id  = "same"
env_key_id     = "K2"
env_secret_key = "S2"
base_url   = "https://paper-api.alpaca.markets"
`
	path := writeTempTOML(t, content)
	_, err := LoadAccounts(path)
	if err == nil {
		t.Fatal("expected error for duplicate tenant_id")
	}
}

func TestLoadAccounts_EmptyFile(t *testing.T) {
	path := writeTempTOML(t, "# empty\n")
	_, err := LoadAccounts(path)
	if err == nil {
		t.Fatal("expected error for empty accounts list")
	}
}

func TestLoadAccounts_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "missing label",
			content: `
[[accounts]]
tenant_id  = "t1"
env_key_id     = "K"
env_secret_key = "S"
base_url   = "https://paper-api.alpaca.markets"
`,
		},
		{
			name: "missing tenant_id",
			content: `
[[accounts]]
label      = "l1"
env_key_id     = "K"
env_secret_key = "S"
base_url   = "https://paper-api.alpaca.markets"
`,
		},
		{
			name: "missing env_key_id",
			content: `
[[accounts]]
label      = "l1"
tenant_id  = "t1"
env_secret_key = "S"
base_url   = "https://paper-api.alpaca.markets"
`,
		},
		{
			name: "missing base_url",
			content: `
[[accounts]]
label      = "l1"
tenant_id  = "t1"
env_key_id     = "K"
env_secret_key = "S"
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempTOML(t, tt.content)
			_, err := LoadAccounts(path)
			if err == nil {
				t.Fatalf("expected validation error for %s", tt.name)
			}
		})
	}
}

func TestLoadAccounts_FileNotFound(t *testing.T) {
	_, err := LoadAccounts("/nonexistent/accounts.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAccountConfig_ResolvedCredentials(t *testing.T) {
	t.Setenv("TEST_ACCT_KEY", "my-key-123")
	t.Setenv("TEST_ACCT_SECRET", "my-secret-456")

	acct := AccountConfig{
		Label:        "test",
		TenantID:     "test",
		EnvKeyID:     "TEST_ACCT_KEY",
		EnvSecretKey: "TEST_ACCT_SECRET",
		BaseURL:      "https://paper-api.alpaca.markets",
		PaperMode:    true,
	}

	if got := acct.ResolvedAPIKeyID(); got != "my-key-123" {
		t.Errorf("expected 'my-key-123', got %q", got)
	}
	if got := acct.ResolvedAPISecretKey(); got != "my-secret-456" {
		t.Errorf("expected 'my-secret-456', got %q", got)
	}
}

func TestAccountConfig_ToAlpacaConfig(t *testing.T) {
	t.Setenv("TEST_KEY", "ak")
	t.Setenv("TEST_SECRET", "as")

	acct := AccountConfig{
		Label:        "test",
		TenantID:     "test",
		EnvKeyID:     "TEST_KEY",
		EnvSecretKey: "TEST_SECRET",
		BaseURL:      "https://paper-api.alpaca.markets",
		PaperMode:    true,
	}
	alpCfg := acct.ToAlpacaConfig()
	if alpCfg.APIKeyID != "ak" {
		t.Errorf("expected APIKeyID 'ak', got %q", alpCfg.APIKeyID)
	}
	if alpCfg.APISecretKey != "as" {
		t.Errorf("expected APISecretKey 'as', got %q", alpCfg.APISecretKey)
	}
	if alpCfg.BaseURL != "https://paper-api.alpaca.markets" {
		t.Errorf("unexpected BaseURL %q", alpCfg.BaseURL)
	}
	if !alpCfg.PaperMode {
		t.Error("expected PaperMode true")
	}
}

func writeTempTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
