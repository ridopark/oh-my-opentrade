package config

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// SymbolMeta carries the GICS-style classification for a single ticker.
// Loaded from configs/symbol_metadata.toml at startup and consumed by the
// Sprint 4 sector/industry exposure gate.
type SymbolMeta struct {
	Sector   string `toml:"sector"`
	Industry string `toml:"industry"`
}

// SymbolMetadata is the lookup map keyed by ticker symbol (uppercase).
type SymbolMetadata map[string]SymbolMeta

// LoadSymbolMetadata decodes the metadata TOML file into a map keyed by symbol.
// Missing file or decode errors are wrapped for the caller.
func LoadSymbolMetadata(path string) (SymbolMetadata, error) {
	var m SymbolMetadata
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("symbol_metadata: load %s: %w", path, err)
	}
	return m, nil
}

// MissingSymbols returns the subset of active that have no entry in m.
// Intended for startup-time warnings so operators know which tickers the
// exposure gate will fail-open on.
func (m SymbolMetadata) MissingSymbols(active []string) []string {
	var missing []string
	for _, s := range active {
		if _, ok := m[s]; !ok {
			missing = append(missing, s)
		}
	}
	return missing
}
