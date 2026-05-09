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
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
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

	args := []string{
		"--backtest",
		"--no-ai",
		"--strategies=tradingthetrend_v1",
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
