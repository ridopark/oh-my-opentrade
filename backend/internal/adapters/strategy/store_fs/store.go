package store_fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
)

type LoadFunc func(path string) (portstrategy.Spec, error)

type Store struct {
	dir          string
	loadFn       LoadFunc
	pollInterval time.Duration

	mu       sync.RWMutex
	byPath   map[string]cached
	byKey    map[string]string
	allPaths []string
}

type cached struct {
	spec  portstrategy.Spec
	mtime time.Time
}

func NewStore(dir string, loadFn LoadFunc) *Store {
	return NewStoreWithPollInterval(dir, loadFn, 5*time.Second)
}

func NewStoreWithPollInterval(dir string, loadFn LoadFunc, pollInterval time.Duration) *Store {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	return &Store{
		dir:          dir,
		loadFn:       loadFn,
		pollInterval: pollInterval,
		byPath:       make(map[string]cached),
		byKey:        make(map[string]string),
	}
}

func (s *Store) List(ctx context.Context, filter *portstrategy.SpecFilter) ([]portstrategy.Spec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := s.scan()
	if err != nil {
		return nil, err
	}

	result := make([]portstrategy.Spec, 0, len(paths))
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		spec, err := s.loadIfNeeded(p)
		if err != nil {
			return nil, err
		}
		if !matchesFilter(spec, filter) {
			continue
		}
		result = append(result, spec)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID.String() < result[j].ID.String()
		}
		return compareVersions(result[i].Version, result[j].Version) < 0
	})

	return result, nil
}

func (s *Store) Get(ctx context.Context, id domstrategy.StrategyID, version domstrategy.Version) (*portstrategy.Spec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := s.scan()
	if err != nil {
		return nil, err
	}

	targetKey := specKey(id, version)
	s.mu.RLock()
	if p, ok := s.byKey[targetKey]; ok {
		s.mu.RUnlock()
		spec, err := s.loadIfNeeded(p)
		if err == nil {
			cp := spec
			return &cp, nil
		}
	} else {
		s.mu.RUnlock()
	}

	for _, p := range paths {
		spec, err := s.loadIfNeeded(p)
		if err != nil {
			return nil, err
		}
		if spec.ID == id && spec.Version == version {
			cp := spec
			return &cp, nil
		}
	}

	return nil, fmt.Errorf("%w: %s@%s", domstrategy.ErrStrategyNotFound, id, version)
}

func (s *Store) GetLatest(ctx context.Context, id domstrategy.StrategyID) (*portstrategy.Spec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := s.scan()
	if err != nil {
		return nil, err
	}

	var best *portstrategy.Spec
	for _, p := range paths {
		spec, err := s.loadIfNeeded(p)
		if err != nil {
			return nil, err
		}
		if spec.ID != id {
			continue
		}
		if best == nil || compareVersions(spec.Version, best.Version) > 0 {
			cp := spec
			best = &cp
		}
	}

	if best == nil {
		return nil, fmt.Errorf("%w: %s", domstrategy.ErrStrategyNotFound, id)
	}
	return best, nil
}

func (s *Store) Save(ctx context.Context, spec portstrategy.Spec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("strategy spec store: mkdir %q: %w", s.dir, err)
	}

	path := filepath.Join(s.dir, fileNameFor(spec.ID, spec.Version))
	content, err := encodeV2(spec)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("strategy spec store: write %q: %w", path, err)
	}

	info, err := os.Stat(path)
	if err == nil {
		s.mu.Lock()
		s.byPath[path] = cached{spec: spec, mtime: info.ModTime()}
		s.byKey[specKey(spec.ID, spec.Version)] = path
		s.mu.Unlock()
	}

	return nil
}

func (s *Store) Watch(ctx context.Context) (<-chan domstrategy.StrategyID, error) {
	ch := make(chan domstrategy.StrategyID, 16)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(s.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				paths, err := s.scan()
				if err != nil {
					continue
				}
				for _, p := range paths {
					info, err := os.Stat(p)
					if err != nil {
						continue
					}

					s.mu.RLock()
					c, ok := s.byPath[p]
					s.mu.RUnlock()

					if ok && !info.ModTime().After(c.mtime) {
						continue
					}
					spec, err := s.loadIfNeeded(p)
					if err != nil {
						continue
					}

					select {
					case <-ctx.Done():
						return
					case ch <- spec.ID:
					}
				}
			}
		}
	}()

	return ch, nil
}

func matchesFilter(spec portstrategy.Spec, filter *portstrategy.SpecFilter) bool {
	if filter == nil {
		return true
	}
	if filter.LifecycleState != nil {
		if spec.Lifecycle.State != *filter.LifecycleState {
			return false
		}
	}
	if filter.Author != "" {
		if spec.Author != filter.Author {
			return false
		}
	}
	return true
}

func (s *Store) scan() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("strategy spec store: readdir %q: %w", s.dir, err)
	}

	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".toml") {
			continue
		}
		paths = append(paths, filepath.Join(s.dir, name))
	}
	sort.Strings(paths)

	s.mu.Lock()
	s.allPaths = paths
	known := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		known[p] = struct{}{}
	}
	for p, c := range s.byPath {
		if _, ok := known[p]; ok {
			continue
		}
		delete(s.byPath, p)
		delete(s.byKey, specKey(c.spec.ID, c.spec.Version))
	}
	s.mu.Unlock()

	return paths, nil
}

func (s *Store) loadIfNeeded(path string) (portstrategy.Spec, error) {
	info, err := os.Stat(path)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec store: stat %q: %w", path, err)
	}

	s.mu.RLock()
	if c, ok := s.byPath[path]; ok && !info.ModTime().After(c.mtime) {
		s.mu.RUnlock()
		return c.spec, nil
	}
	loadFn := s.loadFn
	s.mu.RUnlock()

	if loadFn == nil {
		return portstrategy.Spec{}, errors.New("strategy spec store: loadFn is nil")
	}

	spec, err := loadFn(path)
	if err != nil {
		return portstrategy.Spec{}, err
	}

	s.mu.Lock()
	s.byPath[path] = cached{spec: spec, mtime: info.ModTime()}
	s.byKey[specKey(spec.ID, spec.Version)] = path
	s.mu.Unlock()

	return spec, nil
}

func specKey(id domstrategy.StrategyID, version domstrategy.Version) string {
	return id.String() + "@" + version.String()
}

func fileNameFor(id domstrategy.StrategyID, version domstrategy.Version) string {
	return fmt.Sprintf("%s__%s.toml", id.String(), version.String())
}

func encodeV2(spec portstrategy.Spec) ([]byte, error) {
	regime := make(map[string]any)
	params := make(map[string]any)
	for k, v := range spec.Params {
		if strings.HasPrefix(k, "regime_filter.") {
			regime[strings.TrimPrefix(k, "regime_filter.")] = v
			continue
		}
		params[k] = v
	}

	hooks := make(map[string]map[string]string)
	for name, href := range spec.Hooks {
		m := map[string]string{
			"engine": href.Engine.String(),
		}
		if href.Name != "" {
			m["name"] = href.Name
		}
		if href.Entrypoint != "" {
			m["entrypoint"] = href.Entrypoint
		}
		if href.Source != "" {
			m["source"] = href.Source
		}
		hooks[name] = m
	}

	raw := struct {
		SchemaVersion int `toml:"schema_version"`
		Strategy      struct {
			ID          string `toml:"id"`
			Version     string `toml:"version"`
			Name        string `toml:"name"`
			Description string `toml:"description"`
			Author      string `toml:"author"`
		} `toml:"strategy"`
		Lifecycle struct {
			State     string `toml:"state"`
			PaperOnly bool   `toml:"paper_only"`
		} `toml:"lifecycle"`
		Routing struct {
			Symbols            []string `toml:"symbols"`
			Timeframes         []string `toml:"timeframes"`
			Priority           int      `toml:"priority"`
			ConflictPolicy     string   `toml:"conflict_policy"`
			ExclusivePerSymbol bool     `toml:"exclusive_per_symbol"`
		} `toml:"routing"`
		Params       map[string]any               `toml:"params"`
		RegimeFilter map[string]any               `toml:"regime_filter"`
		Hooks        map[string]map[string]string `toml:"hooks"`
	}{
		SchemaVersion: 2,
		Params:        params,
		RegimeFilter:  regime,
		Hooks:         hooks,
	}

	raw.Strategy.ID = spec.ID.String()
	raw.Strategy.Version = spec.Version.String()
	raw.Strategy.Name = spec.Name
	raw.Strategy.Description = spec.Description
	raw.Strategy.Author = spec.Author

	raw.Lifecycle.State = spec.Lifecycle.State.String()
	raw.Lifecycle.PaperOnly = spec.Lifecycle.PaperOnly

	raw.Routing.Symbols = spec.Routing.Symbols
	raw.Routing.Timeframes = spec.Routing.Timeframes
	raw.Routing.Priority = spec.Routing.Priority
	raw.Routing.ConflictPolicy = spec.Routing.ConflictPolicy.String()
	raw.Routing.ExclusivePerSymbol = spec.Routing.ExclusivePerSymbol

	var b strings.Builder
	enc := toml.NewEncoder(&b)
	if err := enc.Encode(raw); err != nil {
		return nil, fmt.Errorf("strategy spec store: encode TOML: %w", err)
	}
	return []byte(b.String()), nil
}

func compareVersions(a, b domstrategy.Version) int {
	amaj, amin, apat, apre, aok := parseSemver(a.String())
	bmaj, bmin, bpat, bpre, bok := parseSemver(b.String())
	if !aok || !bok {
		return strings.Compare(a.String(), b.String())
	}
	if amaj != bmaj {
		if amaj < bmaj {
			return -1
		}
		return 1
	}
	if amin != bmin {
		if amin < bmin {
			return -1
		}
		return 1
	}
	if apat != bpat {
		if apat < bpat {
			return -1
		}
		return 1
	}
	if apre == "" && bpre != "" {
		return 1
	}
	if apre != "" && bpre == "" {
		return -1
	}
	return strings.Compare(apre, bpre)
}

func parseSemver(s string) (major, minor, patch int, pre string, ok bool) {
	main := s
	if idx := strings.IndexByte(s, '-'); idx != -1 {
		main = s[:idx]
		pre = s[idx+1:]
	}
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return 0, 0, 0, "", false
	}
	_, err := fmt.Sscanf(parts[0]+" "+parts[1]+" "+parts[2], "%d %d %d", &major, &minor, &patch)
	if err != nil {
		return 0, 0, 0, "", false
	}
	return major, minor, patch, pre, true
}

var _ portstrategy.SpecStore = (*Store)(nil)
