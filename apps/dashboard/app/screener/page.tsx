"use client";

import React, { useState, useMemo } from "react";

interface ScreenerResult {
  symbol: string;
  price: number;
  atr: number;
  atr_pct: number;
  nr7: boolean;
  bias: string;
  ema200: number;
  realized_vol: number;
  score: number;
}

const PRESETS: Record<string, string[]> = {
  "ORB Candidates": ["AAPL", "MSFT", "GOOGL", "AMZN", "TSLA", "SOXL", "U", "PLTR", "SPY", "META", "HIMS", "SOFI", "NFLX", "QQQ", "BAC", "AMD", "NVDA"],
  "High Beta": ["TSLA", "SOXL", "HIMS", "PLTR", "AMD", "NVDA", "SOFI", "U", "RIVN"],
  "Leveraged ETFs": ["SOXL", "SOXS", "TQQQ", "SQQQ", "TSLL", "UCO"],
  "Mega Cap": ["AAPL", "MSFT", "GOOGL", "AMZN", "META", "NVDA", "TSLA", "NFLX"],
};

type SortKey = keyof ScreenerResult;

export default function ScreenerPage() {
  const [preset, setPreset] = useState("ORB Candidates");
  const [customSymbols, setCustomSymbols] = useState("");
  const [screenDate, setScreenDate] = useState(() => new Date().toISOString().split("T")[0]);
  const [resultDate, setResultDate] = useState("");
  const [scanMode, setScanMode] = useState<"custom" | "universe">("universe");
  const [results, setResults] = useState<ScreenerResult[]>([]);
  const [totalScanned, setTotalScanned] = useState(0);
  const [loading, setLoading] = useState(false);
  const [progress, setProgress] = useState<{ pct: number; done: number; total: number; stage: string } | null>(null);
  const [sortKey, setSortKey] = useState<SortKey>("score");
  const [sortAsc, setSortAsc] = useState(false);
  const [minATR, setMinATR] = useState(0);

  const handleScreen = async () => {
    setLoading(true);
    setProgress(null);
    setResults([]);
    try {
      const symbols = customSymbols.trim()
        ? customSymbols.split(",").map((s) => s.trim()).filter(Boolean)
        : PRESETS[preset] ?? PRESETS["ORB Candidates"];

      if (scanMode === "universe") {
        // SSE streaming for universe mode with progress updates
        let url = "/api/screener?mode=universe&stream=1";
        if (screenDate) url += `&date=${screenDate}`;
        const res = await fetch(url);
        if (!res.ok || !res.body) return;
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split("\n");
          buffer = lines.pop() ?? "";
          for (const line of lines) {
            if (!line.startsWith("data: ")) continue;
            try {
              const data = JSON.parse(line.slice(6));
              if (data.type === "progress") {
                setProgress({ pct: data.pct, done: data.done, total: data.total, stage: data.stage });
              } else if (data.type === "done") {
                setResults(data.results ?? []);
                setResultDate(data.date ?? "");
                setTotalScanned(data.total ?? 0);
                setProgress(null);
              }
            } catch {}
          }
        }
      } else {
        // Direct JSON for custom mode
        let url = `/api/screener?symbols=${encodeURIComponent(symbols.join(","))}`;
        if (screenDate) url += `&date=${screenDate}`;
        const res = await fetch(url);
        if (res.ok) {
          const data = await res.json();
          setResults(data?.results ?? []);
          setResultDate(data?.date ?? "");
          setTotalScanned(data?.total ?? 0);
        }
      }
    } finally {
      setLoading(false);
      setProgress(null);
    }
  };

  const handleSort = (key: SortKey) => {
    if (sortKey === key) setSortAsc(!sortAsc);
    else { setSortKey(key); setSortAsc(false); }
  };

  const sorted = useMemo(() => {
    const filtered = minATR > 0 ? results.filter((r) => r.atr_pct >= minATR) : results;
    return [...filtered].sort((a, b) => {
      const av = a[sortKey] ?? 0;
      const bv = b[sortKey] ?? 0;
      if (typeof av === "string" && typeof bv === "string") return sortAsc ? av.localeCompare(bv) : bv.localeCompare(av);
      return sortAsc ? (av as number) - (bv as number) : (bv as number) - (av as number);
    });
  }, [results, sortKey, sortAsc, minATR]);

  const SortHeader = ({ label, field, title }: { label: string; field: SortKey; title?: string }) => (
    <th
      className={`px-3 py-2 text-left cursor-pointer hover:text-foreground transition-colors select-none ${title ? "cursor-help" : ""}`}
      onClick={() => handleSort(field)}
      title={title}
    >
      {label} {sortKey === field ? (sortAsc ? "^" : "v") : ""}
    </th>
  );

  return (
    <div className="flex flex-col gap-4 p-4 max-w-[1400px]">
      <div className="flex items-center gap-3">
        <h1 className="text-lg font-bold text-foreground">Screener</h1>
        <span className="text-xs text-muted-foreground">ORB symbol fitness analysis</span>
      </div>

      {/* Controls */}
      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-card p-3">
        <div className="flex items-center gap-1 rounded border border-border overflow-hidden">
          <button
            onClick={() => setScanMode("custom")}
            className={`px-3 py-1.5 text-xs font-medium transition-colors ${scanMode === "custom" ? "bg-emerald-600 text-white" : "text-muted-foreground hover:text-foreground"}`}
          >
            Custom
          </button>
          <button
            onClick={() => setScanMode("universe")}
            className={`px-3 py-1.5 text-xs font-medium transition-colors ${scanMode === "universe" ? "bg-emerald-600 text-white" : "text-muted-foreground hover:text-foreground"}`}
            title="Scan all ~3,000 tradeable equities from Alpaca, filter by price/volume, rank top 50"
          >
            Universe
          </button>
        </div>

        {scanMode === "custom" && <>
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">Preset</span>
            <select
              value={preset}
              onChange={(e) => { setPreset(e.target.value); setCustomSymbols(""); }}
              className="bg-background border border-border rounded px-2 py-1 text-xs font-mono text-foreground"
            >
              {Object.keys(PRESETS).map((p) => <option key={p} value={p}>{p}</option>)}
              <option value="custom">Custom</option>
            </select>
          </div>
          <div className="flex items-center gap-2 flex-1 min-w-[200px]">
            <span className="text-xs text-muted-foreground">Symbols</span>
            <input
              type="text"
              value={customSymbols || (preset !== "custom" ? (PRESETS[preset] ?? []).join(", ") : "")}
              onChange={(e) => { setCustomSymbols(e.target.value); setPreset("custom"); }}
              className="flex-1 bg-background border border-border rounded px-2 py-1 text-xs font-mono text-foreground"
              placeholder="AAPL, TSLA, HIMS..."
            />
          </div>
        </>}
        {scanMode === "universe" && (
          <span className="text-xs text-muted-foreground">Scans ~3,000 tradeable equities, filters by price/volume, shows top 50</span>
        )}

        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">Date</span>
          <input
            type="date"
            value={screenDate}
            onChange={(e) => setScreenDate(e.target.value)}
            className="bg-background border border-border rounded px-2 py-1 text-xs font-mono text-foreground"
            placeholder="Today"
          />
        </div>

        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">Min ATR%</span>
          <input
            type="number"
            value={minATR || ""}
            onChange={(e) => setMinATR(parseFloat(e.target.value) || 0)}
            className="w-16 bg-background border border-border rounded px-2 py-1 text-xs font-mono text-foreground"
            placeholder="0.8"
            step="0.1"
          />
        </div>

        <button
          onClick={handleScreen}
          disabled={loading}
          className="px-4 py-1.5 rounded bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-medium disabled:opacity-50 transition-colors"
        >
          {loading ? "Screening..." : "Screen"}
        </button>
      </div>

      {/* Legend */}
      <details className="rounded-lg border border-border bg-card text-xs">
        <summary className="px-3 py-2 cursor-pointer text-muted-foreground hover:text-foreground select-none font-medium">
          Legend & Scoring Guide
        </summary>
        <div className="px-3 pb-3 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 text-muted-foreground">
          <div>
            <div className="font-medium text-foreground mb-1">ATR% (Average True Range)</div>
            <p>How much the stock moves per day as a percentage of price. Higher ATR% = bigger breakout potential, but wider stops needed.</p>
            <div className="flex items-center gap-2 mt-1">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-bold bg-emerald-500/30 text-emerald-300">5%+</span>
              <span className="text-[10px]">High volatility — strong ORB candidate</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-bold bg-emerald-500/20 text-emerald-400">2-5%</span>
              <span className="text-[10px]">Good volatility — solid ORB candidate</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-bold bg-amber-500/20 text-amber-400">0.8-2%</span>
              <span className="text-[10px]">Moderate — may work for ORB</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-bold bg-red-500/20 text-red-400">&lt;0.8%</span>
              <span className="text-[10px]">Low volatility — poor ORB candidate</span>
            </div>
          </div>

          <div>
            <div className="font-medium text-foreground mb-1">NR7 (Narrow Range 7)</div>
            <p>{"YES means yesterday's range was the narrowest in 7 sessions. Volatility compression typically precedes expansion — a breakout is more likely the next day."}</p>
            <div className="flex items-center gap-2 mt-1">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-bold bg-emerald-500/30 text-emerald-300">YES</span>
              <span className="text-[10px]">Compression day — high breakout probability</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="text-muted-foreground/50 text-[10px]">-</span>
              <span className="text-[10px] ml-1">Normal range — no compression signal</span>
            </div>
          </div>

          <div>
            <div className="font-medium text-foreground mb-1">EMA200 Bias</div>
            <p>Price position relative to the 200-day EMA. Indicates the long-term trend direction.</p>
            <div className="flex items-center gap-2 mt-1">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-emerald-500/20 text-emerald-400">BULLISH</span>
              <span className="text-[10px]">Price above EMA200 — favor long breakouts</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-red-500/20 text-red-400">BEARISH</span>
              <span className="text-[10px]">Price below EMA200 — favor short breakouts</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-medium bg-gray-500/20 text-gray-400">NEUTRAL</span>
              <span className="text-[10px]">Price near EMA200 — no directional edge</span>
            </div>
          </div>

          <div>
            <div className="font-medium text-foreground mb-1">Realized Vol (RVol)</div>
            <p>20-day annualized volatility from daily returns. Like a per-stock VIX — measures the current volatility environment.</p>
            <div className="mt-1 text-[10px]">
              <div>&gt;50% — Extremely volatile (crisis-level)</div>
              <div>30-50% — High volatility</div>
              <div>15-30% — Normal</div>
              <div>&lt;15% — Low volatility (calm market)</div>
            </div>
          </div>

          <div>
            <div className="font-medium text-foreground mb-1">Composite Score</div>
            <p>ORB fitness score combining multiple signals:</p>
            <div className="mt-1 text-[10px] font-mono">
              <div>Score = ATR% x 10</div>
              <div>{"      + NR7 bonus (+20 if YES)"}</div>
              <div>{"      + Trend bonus (+5 if BULLISH or BEARISH)"}</div>
            </div>
            <div className="flex items-center gap-2 mt-1">
              <span className="inline-block px-2 py-0.5 rounded text-[10px] font-bold bg-emerald-500/30 text-emerald-300">80+</span>
              <span className="text-[10px]">Strong ORB candidate</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="inline-block px-2 py-0.5 rounded text-[10px] font-bold bg-amber-500/20 text-amber-400">40-80</span>
              <span className="text-[10px]">Moderate candidate</span>
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="inline-block px-2 py-0.5 rounded text-[10px] font-bold bg-gray-500/20 text-gray-400">&lt;40</span>
              <span className="text-[10px]">Weak — low breakout potential</span>
            </div>
          </div>

          <div>
            <div className="font-medium text-foreground mb-1">How to Use</div>
            <p>Run the screener pre-market to pick the best ORB symbols for the day:</p>
            <ol className="mt-1 text-[10px] list-decimal list-inside space-y-0.5">
              <li>Sort by Score (default) to find top candidates</li>
              <li>Look for NR7 = YES symbols (compression breakout)</li>
              <li>Check Bias aligns with your trade direction</li>
              <li>Filter ATR% &gt; 2% for the strongest movers</li>
              <li>Click a symbol to view its chart</li>
              <li>Use historical dates to validate screener picks against past ORB results</li>
            </ol>
          </div>
        </div>
      </details>

      {/* Results */}
      {results.length === 0 && !loading && (
        <div className="flex items-center justify-center h-40 rounded-lg border border-border bg-card text-muted-foreground text-sm">
          Select symbols and click Screen to analyze
        </div>
      )}

      {loading && (
        <div className="flex flex-col items-center justify-center gap-3 h-40 rounded-lg border border-border bg-card text-muted-foreground text-sm">
          {/* Spinner */}
          <div className="w-6 h-6 border-2 border-emerald-500/30 border-t-emerald-500 rounded-full animate-spin" />
          {progress ? (
            <>
              <div className="text-foreground font-medium">{progress.stage}</div>
              <div className="w-64 h-2 bg-muted rounded-full overflow-hidden">
                <div
                  className="h-full bg-emerald-500 transition-all duration-300"
                  style={{ width: `${progress.pct}%` }}
                />
              </div>
              <div className="text-[10px]">{progress.done} / {progress.total} symbols ({progress.pct}%)</div>
            </>
          ) : (
            <div>{scanMode === "universe" ? "Preparing universe scan..." : "Fetching daily bars..."}</div>
          )}
        </div>
      )}

      {sorted.length > 0 && (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-xs font-mono">
              <thead className="bg-card border-b border-border">
                <tr className="text-[10px] text-muted-foreground uppercase">
                  <th className="px-3 py-2 text-left">#</th>
                  <SortHeader label="Symbol" field="symbol" title="Ticker symbol — click to open Trading Signals chart" />
                  <SortHeader label="Price" field="price" title="Latest daily closing price" />
                  <SortHeader label="ATR" field="atr" title="14-day Average True Range in dollars — how much the stock moves per day on average" />
                  <SortHeader label="ATR%" field="atr_pct" title="Daily ATR as % of price — higher = more volatile, better for ORB. Determines stop distance and position size. >2% = strong mover, <1% = slow" />
                  <SortHeader label="NR7" field="nr7" title="Narrow Range 7 — YES means prior day had the narrowest range in 7 sessions. Volatility compression precedes expansion, making breakouts more likely" />
                  <SortHeader label="Bias" field="bias" title="Daily EMA200 trend: BULLISH = price above 200-day EMA (favor longs), BEARISH = below (favor shorts), NEUTRAL = near the line" />
                  <SortHeader label="EMA200" field="ema200" title="200-day Exponential Moving Average — the long-term trend anchor. Price above = uptrend, below = downtrend" />
                  <SortHeader label="RVol" field="realized_vol" title="20-day annualized realized volatility — like a per-stock VIX. >30% = high vol environment, <15% = calm" />
                  <SortHeader label="Score" field="score" title="Composite ORB fitness: ATR%*10 + NR7 bonus(+20) + trending bonus(+5). Higher = better ORB candidate. >80 = strong, 40-80 = moderate, <40 = weak" />
                </tr>
              </thead>
              <tbody>
                {sorted.map((r, i) => {
                  const passATR = r.atr_pct >= 0.8;
                  return (
                    <tr key={r.symbol} className="border-t border-border/30 hover:bg-muted/30 transition-colors">
                      <td className="px-3 py-1.5 text-muted-foreground">{i + 1}</td>
                      <td className="px-3 py-1.5">
                        <a href={`/?symbol=${r.symbol}`} className="text-foreground hover:text-blue-400 hover:underline font-medium">
                          {r.symbol}
                        </a>
                      </td>
                      <td className="px-3 py-1.5 text-right text-foreground">${r.price.toFixed(2)}</td>
                      <td className="px-3 py-1.5 text-right text-muted-foreground">${r.atr.toFixed(2)}</td>
                      <td className="px-3 py-1.5 text-right">
                        <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-bold ${
                          r.atr_pct >= 5 ? "bg-emerald-500/30 text-emerald-300" :
                          r.atr_pct >= 2 ? "bg-emerald-500/20 text-emerald-400" :
                          passATR ? "bg-amber-500/20 text-amber-400" :
                          "bg-red-500/20 text-red-400"
                        }`}>
                          {r.atr_pct.toFixed(1)}%
                        </span>
                      </td>
                      <td className="px-3 py-1.5">
                        {r.nr7 ? (
                          <span className="inline-block px-1.5 py-0.5 rounded text-[10px] font-bold bg-emerald-500/30 text-emerald-300">YES</span>
                        ) : (
                          <span className="text-muted-foreground/50">-</span>
                        )}
                      </td>
                      <td className="px-3 py-1.5">
                        <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-medium ${
                          r.bias === "BULLISH" ? "bg-emerald-500/20 text-emerald-400" :
                          r.bias === "BEARISH" ? "bg-red-500/20 text-red-400" :
                          "bg-gray-500/20 text-gray-400"
                        }`}>
                          {r.bias}
                        </span>
                      </td>
                      <td className="px-3 py-1.5 text-right text-muted-foreground">${r.ema200.toFixed(2)}</td>
                      <td className="px-3 py-1.5 text-right text-muted-foreground">{r.realized_vol.toFixed(1)}%</td>
                      <td className="px-3 py-1.5 text-right">
                        <span className={`inline-block px-2 py-0.5 rounded text-[10px] font-bold ${
                          r.score >= 80 ? "bg-emerald-500/30 text-emerald-300" :
                          r.score >= 40 ? "bg-amber-500/20 text-amber-400" :
                          "bg-gray-500/20 text-gray-400"
                        }`}>
                          {r.score.toFixed(0)}
                        </span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>

          {/* Summary */}
          <div className="flex items-center gap-4 px-3 py-2 border-t border-border text-[10px] text-muted-foreground">
            {resultDate && <span className="font-medium text-foreground">{resultDate}</span>}
            {totalScanned > 0 && <span>Scanned: {totalScanned}</span>}
            <span>Showing: {sorted.length}</span>
            <span>{"ATR% pass (>=0.8%): "}{sorted.filter((r) => r.atr_pct >= 0.8).length}</span>
            <span>NR7 compression: {sorted.filter((r) => r.nr7).length}</span>
            <span>Bullish: {sorted.filter((r) => r.bias === "BULLISH").length}</span>
            <span>Bearish: {sorted.filter((r) => r.bias === "BEARISH").length}</span>
          </div>
        </div>
      )}
    </div>
  );
}
