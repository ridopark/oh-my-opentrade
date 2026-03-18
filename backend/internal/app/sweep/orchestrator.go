package sweep

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/config"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	domsweep "github.com/oh-my-opentrade/backend/internal/domain/sweep"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"

	"database/sql"
)

type Orchestrator struct {
	db          *sql.DB
	appCfg      *config.Config
	marketData  ports.MarketDataPort
	specStore   portstrategy.SpecStore
	strategyDir string
	log         zerolog.Logger

	mu       sync.RWMutex
	sessions map[string]*session
}

type session struct {
	id       string
	config   domsweep.SweepConfig
	cancelFn context.CancelFunc
	status   string

	mu      sync.RWMutex
	clients []chan ports.SweepEvent
	result  *domsweep.SweepResult
	runs    []domsweep.SweepRunResult
}

func NewOrchestrator(db *sql.DB, appCfg *config.Config, marketData ports.MarketDataPort, specStore portstrategy.SpecStore, strategyDir string, log zerolog.Logger) *Orchestrator {
	return &Orchestrator{
		db:          db,
		appCfg:      appCfg,
		marketData:  marketData,
		specStore:   specStore,
		strategyDir: strategyDir,
		log:         log.With().Str("component", "sweep").Logger(),
		sessions:    make(map[string]*session),
	}
}

func (o *Orchestrator) Start(ctx context.Context, cfg domsweep.SweepConfig) (string, error) {
	grid := domsweep.GenerateGrid(cfg.Ranges)
	if len(grid) == 0 {
		return "", fmt.Errorf("empty parameter grid")
	}

	id := generateSweepID()
	sweepCtx, cancel := context.WithCancel(ctx)

	sess := &session{
		id:       id,
		config:   cfg,
		cancelFn: cancel,
		status:   "running",
		clients:  make([]chan ports.SweepEvent, 0),
		runs:     make([]domsweep.SweepRunResult, 0, len(grid)),
	}

	o.mu.Lock()
	o.sessions[id] = sess
	o.mu.Unlock()

	go o.runSweep(sweepCtx, sess, grid)

	return id, nil
}

func (o *Orchestrator) runSweep(ctx context.Context, sess *session, grid []map[string]any) {
	start := time.Now()
	total := len(grid)
	concurrency := sess.config.MaxConcurrency
	if concurrency <= 0 || concurrency > 8 {
		concurrency = 4
	}
	if concurrency > total {
		concurrency = total
	}

	type workItem struct {
		index  int
		params map[string]any
	}

	work := make(chan workItem, total)
	for i, p := range grid {
		work <- workItem{index: i, params: p}
	}
	close(work)

	resultsCh := make(chan domsweep.SweepRunResult, total)
	var wg sync.WaitGroup

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range work {
				if ctx.Err() != nil {
					return
				}
				result := o.runSingle(ctx, sess.config, item.index, item.params)
				resultsCh <- result
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	completed := 0
	for run := range resultsCh {
		completed++
		sess.mu.Lock()
		sess.runs = append(sess.runs, run)
		sess.mu.Unlock()

		sess.broadcast(ports.SweepEvent{Type: "sweep:run_complete", Data: run})
		sess.broadcast(ports.SweepEvent{Type: "sweep:progress", Data: map[string]any{
			"completed": completed, "total": total,
			"pct": float64(completed) / float64(total) * 100,
		}})
	}

	ranked := domsweep.RankRuns(sess.runs, sess.config.TargetMetric, false)
	bestIdx := 0
	if len(ranked) > 0 {
		bestIdx = ranked[0].Index
	}

	finalResult := &domsweep.SweepResult{
		Config:        sess.config,
		Runs:          ranked,
		BestIndex:     bestIdx,
		TotalDuration: time.Since(start),
	}

	sess.mu.Lock()
	sess.result = finalResult
	sess.status = "completed"
	sess.mu.Unlock()

	sess.broadcast(ports.SweepEvent{Type: "sweep:done", Data: finalResult})
	o.log.Info().Str("sweep_id", sess.id).Int("runs", total).Dur("duration", finalResult.TotalDuration).Msg("sweep completed")
}

func (o *Orchestrator) runSingle(ctx context.Context, cfg domsweep.SweepConfig, index int, params map[string]any) domsweep.SweepRunResult {
	start := time.Now()

	spec, err := o.specStore.GetLatest(ctx, domstrategy.StrategyID(cfg.BacktestConfig.Strategies[0]))
	if err != nil {
		o.log.Error().Err(err).Int("index", index).Msg("failed to load spec for sweep run")
		return domsweep.SweepRunResult{Index: index, Params: params, Duration: time.Since(start)}
	}

	mergedParams := make(map[string]any, len(spec.Params))
	for k, v := range spec.Params {
		mergedParams[k] = v
	}
	for k, v := range params {
		mergedParams[k] = v
	}
	spec.Params = mergedParams

	exitRuleParams := make(map[string]float64)
	for k, v := range params {
		if fv, ok := v.(float64); ok {
			exitRuleParams[k] = fv
		}
	}
	for i, er := range spec.ExitRules {
		for pKey := range er.Params {
			if newVal, ok := exitRuleParams[pKey]; ok {
				spec.ExitRules[i].Params[pKey] = newVal
			}
		}
	}

	tmpDir, tmpErr := os.MkdirTemp("", "sweep-*")
	if tmpErr != nil {
		o.log.Error().Err(tmpErr).Msg("failed to create temp dir")
		return domsweep.SweepRunResult{Index: index, Params: params, Duration: time.Since(start)}
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tomlBytes, encErr := store_fs.EncodeFullV2(*spec)
	if encErr != nil {
		o.log.Error().Err(encErr).Msg("failed to encode spec for sweep")
		return domsweep.SweepRunResult{Index: index, Params: params, Duration: time.Since(start)}
	}
	tomlPath := filepath.Join(tmpDir, spec.ID.String()+".toml")
	if writeErr := os.WriteFile(tomlPath, tomlBytes, 0o644); writeErr != nil {
		o.log.Error().Err(writeErr).Msg("failed to write temp TOML")
		return domsweep.SweepRunResult{Index: index, Params: params, Duration: time.Since(start)}
	}

	runCfg := cfg.BacktestConfig
	runCfg.StrategyDir = tmpDir
	runCfg.Speed = "max"

	runner := backtest.NewRunner(runCfg, o.db, o.appCfg, o.marketData, o.log.With().Int("sweep_run", index).Logger())
	if runErr := runner.Run(ctx); runErr != nil {
		o.log.Warn().Err(runErr).Int("index", index).Msg("sweep run failed")
	}

	result := runner.GetResult()
	var metrics backtest.Result
	if result != nil {
		metrics = *result
	}

	return domsweep.SweepRunResult{
		Index:    index,
		Params:   params,
		Metrics:  metrics,
		Duration: time.Since(start),
	}
}

func (o *Orchestrator) Events(ctx context.Context, sweepID string) (<-chan ports.SweepEvent, error) {
	o.mu.RLock()
	sess, ok := o.sessions[sweepID]
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sweep %q not found", sweepID)
	}

	ch := make(chan ports.SweepEvent, 64)

	sess.mu.Lock()
	for _, run := range sess.runs {
		ch <- ports.SweepEvent{Type: "sweep:run_complete", Data: run}
	}
	ch <- ports.SweepEvent{Type: "sweep:progress", Data: map[string]any{
		"completed": len(sess.runs),
		"total":     domsweep.TotalRuns(sess.config.Ranges),
		"pct":       float64(len(sess.runs)) / float64(domsweep.TotalRuns(sess.config.Ranges)) * 100,
	}}
	if sess.result != nil {
		ch <- ports.SweepEvent{Type: "sweep:done", Data: sess.result}
	}
	sess.clients = append(sess.clients, ch)
	sess.mu.Unlock()

	go func() {
		<-ctx.Done()
		sess.mu.Lock()
		for i, c := range sess.clients {
			if c == ch {
				sess.clients = append(sess.clients[:i], sess.clients[i+1:]...)
				break
			}
		}
		sess.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

func (o *Orchestrator) Cancel(sweepID string) error {
	o.mu.RLock()
	sess, ok := o.sessions[sweepID]
	o.mu.RUnlock()
	if !ok {
		return fmt.Errorf("sweep %q not found", sweepID)
	}
	sess.cancelFn()
	sess.mu.Lock()
	sess.status = "canceled"
	sess.mu.Unlock()
	return nil
}

func (o *Orchestrator) GetResult(sweepID string) (*domsweep.SweepResult, error) {
	o.mu.RLock()
	sess, ok := o.sessions[sweepID]
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sweep %q not found", sweepID)
	}
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.result, nil
}

func (o *Orchestrator) ApplyBest(ctx context.Context, sweepID string, runIndex int) error {
	o.mu.RLock()
	sess, ok := o.sessions[sweepID]
	o.mu.RUnlock()
	if !ok {
		return fmt.Errorf("sweep %q not found", sweepID)
	}

	sess.mu.RLock()
	if sess.result == nil {
		sess.mu.RUnlock()
		return fmt.Errorf("sweep not completed")
	}
	if runIndex < 0 || runIndex >= len(sess.result.Runs) {
		sess.mu.RUnlock()
		return fmt.Errorf("run index %d out of range", runIndex)
	}
	run := sess.result.Runs[runIndex]
	sess.mu.RUnlock()

	strategyID := sess.config.StrategyID
	spec, err := o.specStore.GetLatest(ctx, domstrategy.StrategyID(strategyID))
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}

	for k, v := range run.Params {
		spec.Params[k] = v
		if fv, ok := v.(float64); ok {
			for i, er := range spec.ExitRules {
				if _, exists := er.Params[k]; exists {
					spec.ExitRules[i].Params[k] = fv
				}
			}
		}
	}

	tomlBytes, err := store_fs.EncodeFullV2(*spec)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	tomlPath := filepath.Join(o.strategyDir, string(strategyID)+".toml")
	bakPath := tomlPath + ".bak"
	if orig, readErr := os.ReadFile(tomlPath); readErr == nil {
		_ = os.WriteFile(bakPath, orig, 0o644)
	}

	tmpPath := tomlPath + ".tmp"
	if err := os.WriteFile(tmpPath, tomlBytes, 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if _, err := strategy.LoadSpecFile(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("validation: %w", err)
	}
	return os.Rename(tmpPath, tomlPath)
}

func (s *session) broadcast(event ports.SweepEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.clients {
		select {
		case ch <- event:
		default:
		}
	}
}

func generateSweepID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "sw-" + hex.EncodeToString(b)
}
