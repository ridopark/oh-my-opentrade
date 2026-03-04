package strategy

import (
"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// StrategyDNA holds the parsed configuration for a single trading strategy.
// This type is internal to the strategy package and distinct from domain.StrategyDNA.
type StrategyDNA struct {
	ID           string
	Version      int
	Description  string
	Parameters   map[string]any
	RegimeFilter RegimeFilter
}

// RegimeFilter holds regime-based filtering rules for a strategy.
type RegimeFilter struct {
	AllowedRegimes    []string
	MinRegimeStrength float64
}

// tomlFile mirrors the raw TOML structure for deserialization.
type tomlFile struct {
	Strategy struct {
		ID          string `toml:"id"`
		Version     int    `toml:"version"`
		Description string `toml:"description"`
	} `toml:"strategy"`
	Parameters   map[string]any `toml:"parameters"`
	RegimeFilter struct {
		AllowedRegimes    []string `toml:"allowed_regimes"`
		MinRegimeStrength float64  `toml:"min_regime_strength"`
	} `toml:"regime_filter"`
}

// DNAManager loads, caches, and hot-reloads strategy DNA files.
type DNAManager struct {
	mu     sync.RWMutex
	loaded map[string]*StrategyDNA // keyed by strategy ID
	mtimes map[string]time.Time    // keyed by file path
}

// NewDNAManager creates a new, empty DNAManager.
func NewDNAManager() *DNAManager {
	return &DNAManager{
		loaded: make(map[string]*StrategyDNA),
		mtimes: make(map[string]time.Time),
	}
}

// Load parses the TOML file at path and stores the result in the manager.
// Returns the parsed StrategyDNA or an error.
func (m *DNAManager) Load(path string) (*StrategyDNA, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("strategy: failed to read file %q: %w", path, err)
	}

	var raw tomlFile
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("strategy: failed to parse TOML %q: %w", path, err)
	}

	if raw.Strategy.ID == "" {
		return nil, errors.New("strategy: DNA file is missing required field strategy.id")
	}

	dna := &StrategyDNA{
		ID:          raw.Strategy.ID,
		Version:     raw.Strategy.Version,
		Description: raw.Strategy.Description,
		Parameters:  raw.Parameters,
		RegimeFilter: RegimeFilter{
			AllowedRegimes:    raw.RegimeFilter.AllowedRegimes,
			MinRegimeStrength: raw.RegimeFilter.MinRegimeStrength,
		},
	}
	if dna.Parameters == nil {
		dna.Parameters = make(map[string]any)
	}

	// Record file modification time for Watch
	if info, err := os.Stat(path); err == nil {
		m.mu.Lock()
		m.mtimes[path] = info.ModTime()
		m.mu.Unlock()
	}

	m.mu.Lock()
	m.loaded[dna.ID] = dna
	m.mu.Unlock()

	return dna, nil
}

// Get retrieves a loaded StrategyDNA by its strategy ID.
// Returns (nil, false) if the ID has not been loaded.
func (m *DNAManager) Get(id string) (*StrategyDNA, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	dna, ok := m.loaded[id]
	return dna, ok
}

// GetAll returns all loaded strategy DNAs.
func (m *DNAManager) GetAll() []*StrategyDNA {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*StrategyDNA, 0, len(m.loaded))
	for _, dna := range m.loaded {
		out = append(out, dna)
	}
	return out
}

// Watch polls path every 5 seconds and calls onChange whenever the file's
// modification time changes. Stops when ctx is cancelled.
func (m *DNAManager) Watch(ctx context.Context, path string, onChange func(*StrategyDNA)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			m.mu.RLock()
			lastMtime := m.mtimes[path]
			m.mu.RUnlock()

			if info.ModTime().After(lastMtime) {
				dna, err := m.Load(path)
				if err != nil {
					continue
				}
				onChange(dna)
			}
		}
	}
}

// UpdateScript reads the TOML file at path, replaces or inserts the
// script key in [parameters], and writes the file back.
// The file watcher picks up the change within its polling interval.
func (m *DNAManager) UpdateScript(path, script string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("strategy: read %q: %w", path, err)
	}

	content := string(data)

	// Remove any existing script = """...""" block.
	for {
		start := strings.Index(content, "script = \"\"\"")
		if start == -1 {
			break
		}
		end := strings.Index(content[start+12:], "\"\"\"")
		if end == -1 {
			break
		}
		content = content[:start] + content[start+12+end+3:]
	}

	// Remove any single-line script = ... or commented # script lines.
	lines := strings.Split(content, "\n")
	filtered := lines[:0]
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "script") || strings.HasPrefix(trimmed, "# script") {
			continue
		}
		filtered = append(filtered, l)
	}
	content = strings.Join(filtered, "\n")

	// Find [parameters] section.
	paramIdx := strings.Index(content, "[parameters]")
	if paramIdx == -1 {
		return fmt.Errorf("strategy: TOML file %q has no [parameters] section", path)
	}

	// Find the end of [parameters] (next section header or EOF).
	rest := content[paramIdx:]
	nextSection := strings.Index(rest[1:], "\n[")
	insertAt := paramIdx + len(rest)
	if nextSection != -1 {
		insertAt = paramIdx + 1 + nextSection
	}

	// Append new script block inside [parameters].
	scriptBlock := fmt.Sprintf("\nscript = \"\"\"\n%s\n\"\"\"\n", script)
	newContent := content[:insertAt] + scriptBlock + content[insertAt:]
	return os.WriteFile(path, []byte(newContent), 0o644)
}
