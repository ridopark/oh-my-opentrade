// omo-tradingthetrend-backtest is a thin wrapper around omo-replay's
// --backtest mode pre-pinned to the tradingthetrend strategy and a
// JSONL history file. Exists per plan section 4b's
// "File: backend/cmd/omo-tradingthetrend-backtest/main.go" requirement;
// all load-bearing logic lives in backtest.Runner via omo-replay's
// canonical entry point. KISS/DRY: this binary owns flag-massaging
// only, then exec's omo-replay with the derived flag set.
//
// Usage:
//
//	omo-tradingthetrend-backtest \
//	  --history _workspace/tradingthetrend_history_YYYYMMDD.jsonl \
//	  --from 2026-02-09 --to 2026-05-08 \
//	  --output-json _workspace/ttt_backtest_results.json
//
// All other flags pass through to omo-replay; pass --help for the full
// surface.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
)

func main() {
	var (
		historyPath   string
		fromFlag      string
		toFlag        string
		outputJSON    string
		configPath    string
		envPath       string
		initialEquity float64
		slippageBPS   int64
		timeframeFlag string
		omoReplayBin  string
	)
	flag.StringVar(&historyPath, "history", "", "Path to tradingthetrend Discord history JSONL (required)")
	flag.StringVar(&fromFlag, "from", "", "Start time (RFC3339 or YYYY-MM-DD)")
	flag.StringVar(&toFlag, "to", "", "End time (RFC3339 or YYYY-MM-DD); defaults to now")
	flag.StringVar(&outputJSON, "output-json", "", "Path to write backtest result JSON")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.Float64Var(&initialEquity, "initial-equity", 100000.0, "Initial account equity")
	flag.Int64Var(&slippageBPS, "slippage-bps", 5, "SimBroker slippage in basis points")
	flag.StringVar(&timeframeFlag, "timeframe", "5m", "Bar timeframe (default: 5m matches TTT TOML)")
	flag.StringVar(&omoReplayBin, "omo-replay-bin", "", "Path to omo-replay binary (default: lookup on PATH)")
	flag.Parse()

	if historyPath == "" {
		fmt.Fprintln(os.Stderr, "--history is required")
		os.Exit(2)
	}
	if _, err := os.Stat(historyPath); err != nil {
		fmt.Fprintf(os.Stderr, "history file not readable: %v\n", err)
		os.Exit(2)
	}

	bin := omoReplayBin
	if bin == "" {
		resolved, err := exec.LookPath("omo-replay")
		if err != nil {
			fmt.Fprintln(os.Stderr, "omo-replay not on PATH; build with `go build ./backend/cmd/omo-replay` or pass --omo-replay-bin")
			os.Exit(2)
		}
		bin = resolved
	}

	tickers, err := tickersFromHistory(historyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ticker extraction failed: %v\n", err)
		os.Exit(2)
	}
	if len(tickers) == 0 {
		fmt.Fprintln(os.Stderr, "no parseable tickers in history file (parser found 0 entry-grammar matches)")
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "watchlist universe: %d tickers (%s)\n", len(tickers), strings.Join(tickers, ","))

	args := []string{
		"--backtest",
		"--no-ai",
		"--strategies=tradingthetrend_v1",
		"--force-active=tradingthetrend_v1",
		"--symbols=" + strings.Join(tickers, ","),
		"--ttt-history=" + historyPath,
		"--config=" + configPath,
		"--env-file=" + envPath,
		fmt.Sprintf("--initial-equity=%.2f", initialEquity),
		fmt.Sprintf("--slippage-bps=%d", slippageBPS),
		"--timeframe=" + timeframeFlag,
	}
	if fromFlag != "" {
		args = append(args, "--from="+fromFlag)
	}
	if toFlag != "" {
		args = append(args, "--to="+toFlag)
	}
	if outputJSON != "" {
		args = append(args, "--output-json="+outputJSON)
	}

	fmt.Fprintf(os.Stderr, "exec: %s %s\n", bin, strings.Join(args, " "))
	if err := syscall.Exec(bin, append([]string{bin}, args...), os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "exec failed: %v\n", err)
		os.Exit(1)
	}
}

// historyMessage mirrors the JSONL row shape emitted by the scraper.
type historyMessage struct {
	Text string `json:"text"`
}

// tickersFromHistory parses the JSONL via the canonical TTT message
// parser and returns the sorted union of tickers seen in entry-grammar
// matches. This becomes the backtest --symbols universe so bars only
// load for tickers that actually appear in the watchlist.
func tickersFromHistory(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]struct{})
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg historyMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("unmarshal history line: %w", err)
		}
		for _, p := range builtin.ParseTradingTheTrendMessage(msg.Text) {
			seen[p.Ticker] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}
