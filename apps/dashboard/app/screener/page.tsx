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
  const [screenDate, setScreenDate] = useState("");
  const [resultDate, setResultDate] = useState("");
  const [results, setResults] = useState<ScreenerResult[]>([]);
  const [loading, setLoading] = useState(false);
  const [sortKey, setSortKey] = useState<SortKey>("score");
  const [sortAsc, setSortAsc] = useState(false);
  const [minATR, setMinATR] = useState(0);

  const handleScreen = async () => {
    setLoading(true);
    try {
      const symbols = customSymbols.trim()
        ? customSymbols.split(",").map((s) => s.trim()).filter(Boolean)
        : PRESETS[preset] ?? PRESETS["ORB Candidates"];
      let url = `/api/screener?symbols=${encodeURIComponent(symbols.join(","))}`;
      if (screenDate) url += `&date=${screenDate}`;
      const res = await fetch(url);
      if (res.ok) {
        const data = await res.json();
        setResults(data?.results ?? []);
        setResultDate(data?.date ?? "");
      }
    } finally {
      setLoading(false);
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
      className="px-3 py-2 text-left cursor-pointer hover:text-foreground transition-colors select-none"
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

      {/* Results */}
      {results.length === 0 && !loading && (
        <div className="flex items-center justify-center h-40 rounded-lg border border-border bg-card text-muted-foreground text-sm">
          Select symbols and click Screen to analyze
        </div>
      )}

      {loading && (
        <div className="flex items-center justify-center h-40 rounded-lg border border-border bg-card text-muted-foreground text-sm">
          Fetching daily bars and computing indicators...
        </div>
      )}

      {sorted.length > 0 && (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-xs font-mono">
              <thead className="bg-card border-b border-border">
                <tr className="text-[10px] text-muted-foreground uppercase">
                  <th className="px-3 py-2 text-left">#</th>
                  <SortHeader label="Symbol" field="symbol" />
                  <SortHeader label="Price" field="price" />
                  <SortHeader label="ATR" field="atr" title="14-day Average True Range in dollars" />
                  <SortHeader label="ATR%" field="atr_pct" title="Daily ATR as % of price — higher = more volatile, better for ORB breakouts" />
                  <SortHeader label="NR7" field="nr7" title="Narrow Range 7: prior day had narrowest range in 7 sessions — compression precedes expansion" />
                  <SortHeader label="Bias" field="bias" title="Daily EMA200 trend direction: BULLISH (price > EMA200), BEARISH (price < EMA200)" />
                  <SortHeader label="EMA200" field="ema200" />
                  <SortHeader label="RVol" field="realized_vol" title="20-day annualized realized volatility — VIX-like measure for this stock" />
                  <SortHeader label="Score" field="score" title="Composite ORB fitness score: ATR%*10 + NR7 bonus(+20) + trending bonus(+5)" />
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
            <span>{sorted.length} symbols</span>
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
