"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Radio, WifiOff, AlertTriangle } from "lucide-react";
import { LiveSymbolChart } from "@/components/live/live-symbol-chart";
import { SymbolPicker } from "@/components/live/symbol-picker";
import { useSymbols } from "@/lib/use-symbols";
import { useChartData, type OHLCBar } from "@/lib/use-chart-data";
import { cn } from "@/lib/utils";

type EventKind = "bar" | "forming" | "trade";

interface TickEvent {
  id: number;
  kind: EventKind;
  symbol: string;
  price: number;
  ts: number;
  dir: "up" | "down" | "flat";
}

const STALE_MS = 8_000;
const TICK_TAPE_LEN = 18;
const RATE_WINDOW_MS = 5000;

export default function LivePage() {
  const { symbols } = useSymbols();
  const [symbol, setSymbol] = useState<string>("");

  useEffect(() => {
    if (!symbol && symbols.length > 0) setSymbol(symbols[0]);
  }, [symbols, symbol]);

  const symList = useMemo(() => (symbol ? [symbol] : []), [symbol]);
  const { barsBySymbol } = useChartData("1m", "/api/events", symList);
  const bars: OHLCBar[] = symbol ? (barsBySymbol[symbol] ?? []) : [];

  // Event stream for liveness signals (independent of chart data hook)
  const [tickTape, setTickTape] = useState<TickEvent[]>([]);
  const [lastTickAt, setLastTickAt] = useState<number>(0);
  const [tickCount, setTickCount] = useState<number>(0);
  const [connected, setConnected] = useState<boolean>(false);
  const [formingActive, setFormingActive] = useState<boolean>(false);
  const lastPriceRef = useRef<number>(0);
  const idRef = useRef<number>(0);
  const eventRatesRef = useRef<number[]>([]);

  useEffect(() => {
    if (!symbol) return;
    const es = new EventSource("/api/events");
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);

    const push = (kind: EventKind, sym: string, price: number) => {
      if (sym !== symbol) return;
      const now = Date.now();
      const prev = lastPriceRef.current;
      const dir: "up" | "down" | "flat" =
        prev === 0 ? "flat" : price > prev ? "up" : price < prev ? "down" : "flat";
      lastPriceRef.current = price;
      eventRatesRef.current.push(now);
      idRef.current += 1;
      const evt: TickEvent = { id: idRef.current, kind, symbol: sym, price, ts: now, dir };
      setLastTickAt(now);
      setTickCount((c) => c + 1);
      setTickTape((prev) => [evt, ...prev].slice(0, TICK_TAPE_LEN));
      if (kind === "forming") setFormingActive(true);
    };

    const handleBar = (e: MessageEvent) => {
      try {
        const env = JSON.parse(e.data) as {
          type?: string;
          payload: { symbol: string; close: number };
        };
        const p = env.payload;
        if (!p?.symbol || typeof p.close !== "number") return;
        push(env.type === "FormingBar" ? "forming" : "bar", p.symbol, p.close);
      } catch {}
    };
    es.addEventListener("MarketBarSanitized", handleBar);
    es.addEventListener("FormingBar", handleBar);

    return () => es.close();
  }, [symbol]);

  // Periodic re-render to refresh "age" readout without event
  const [, setNowTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setNowTick((n) => (n + 1) % 1_000_000), 150);
    return () => clearInterval(id);
  }, []);

  const now = Date.now();
  const ageMs = lastTickAt === 0 ? Infinity : now - lastTickAt;
  const stale = ageMs > STALE_MS;
  // msgs/sec over rolling window
  eventRatesRef.current = eventRatesRef.current.filter((t) => now - t < RATE_WINDOW_MS);
  const msgsPerSec = eventRatesRef.current.length / (RATE_WINDOW_MS / 1000);

  const lastPrice = lastPriceRef.current || bars.at(-1)?.close || 0;
  const firstClose = bars[0]?.close ?? lastPrice;
  const pctChange = firstClose ? ((lastPrice - firstClose) / firstClose) * 100 : 0;
  const lastEvt = tickTape[0];
  const lastDir = lastEvt?.dir ?? "flat";
  const priceColor =
    lastDir === "up" ? "text-emerald-400" : lastDir === "down" ? "text-red-400" : "text-foreground";
  const glowColor =
    lastDir === "up" ? "rgb(16, 185, 129)" : lastDir === "down" ? "rgb(239, 68, 68)" : "rgb(161, 161, 170)";

  // Ambient border glow intensity driven by msgs/sec (capped)
  const glowIntensity = Math.min(1, msgsPerSec / 5);
  const borderGlow = stale
    ? "0 0 40px -4px rgba(245, 158, 11, 0.35) inset"
    : !connected
      ? "0 0 40px -4px rgba(239, 68, 68, 0.4) inset"
      : `0 0 ${20 + glowIntensity * 40}px -4px rgba(16, 185, 129, ${0.15 + glowIntensity * 0.35}) inset`;

  return (
    <>
      <div
        className="flex h-full flex-col gap-4 rounded-xl border border-border p-4 transition-[box-shadow,border-color] duration-500"
        style={{
          boxShadow: borderGlow,
          borderColor: stale
            ? "rgba(245, 158, 11, 0.5)"
            : !connected
              ? "rgba(239, 68, 68, 0.6)"
              : undefined,
        }}
      >
        {/* Header */}
        <div className="flex flex-wrap items-center justify-between gap-4 shrink-0">
          <div className="flex items-center gap-4">
            <SymbolPicker symbols={symbols} value={symbol} onChange={setSymbol} />
            <div className="relative flex items-baseline gap-3">
              <div
                key={tickCount}
                className={cn("font-mono text-4xl font-bold tabular-nums", priceColor)}
                style={{
                  ["--glow" as string]: glowColor,
                  animation: "live-glow-text 600ms ease-out",
                }}
              >
                {lastPrice > 0 ? lastPrice.toFixed(2) : "—"}
              </div>
              <div
                className={cn(
                  "font-mono text-sm tabular-nums",
                  pctChange > 0 ? "text-emerald-400" : pctChange < 0 ? "text-red-400" : "text-muted-foreground",
                )}
              >
                {pctChange >= 0 ? "+" : ""}
                {pctChange.toFixed(2)}%
              </div>
              {/* Sonar ring — re-triggers on each tick via key */}
              {tickCount > 0 && (
                <span
                  key={`sonar-${tickCount}`}
                  className="pointer-events-none absolute -left-2 top-1/2 h-10 w-10 -translate-y-1/2 rounded-full border-2"
                  style={{
                    borderColor: glowColor,
                    animation: "live-sonar 700ms ease-out forwards",
                  }}
                />
              )}
            </div>
          </div>

          <LiveChip connected={connected} stale={stale} ageMs={ageMs} msgsPerSec={msgsPerSec} />
        </div>

        {/* Chart */}
        <div className="flex-1 min-h-0">
          <LiveSymbolChart bars={bars} forming={formingActive && !stale} stale={stale} />
        </div>

        {/* Diagnostics strip */}
        <DiagnosticsStrip
          tape={tickTape}
          ageMs={ageMs}
          msgsPerSec={msgsPerSec}
          tickCount={tickCount}
          connected={connected}
          stale={stale}
        />

        {/* Banners */}
        {!connected && (
          <Banner
            tone="red"
            icon={<WifiOff className="h-4 w-4" />}
            text="Disconnected from market data stream — reconnecting…"
          />
        )}
        {connected && stale && lastTickAt > 0 && (
          <Banner
            tone="amber"
            icon={<AlertTriangle className="h-4 w-4" />}
            text={`Stale feed — last tick ${Math.round(ageMs / 1000)}s ago`}
          />
        )}
      </div>
    </>
  );
}

function LiveChip({
  connected,
  stale,
  ageMs,
  msgsPerSec,
}: {
  connected: boolean;
  stale: boolean;
  ageMs: number;
  msgsPerSec: number;
}) {
  const label = !connected ? "OFFLINE" : stale ? "STALE" : "LIVE";
  const dotColor = !connected ? "bg-red-500" : stale ? "bg-amber-500" : "bg-emerald-500";
  const ringColor = !connected ? "rgb(239,68,68)" : stale ? "rgb(245,158,11)" : "rgb(16,185,129)";
  // Pulse cadence: faster with higher msgs/sec, floor 1200ms, ceiling 300ms
  const pulseMs = Math.max(300, 1200 - msgsPerSec * 150);
  return (
    <div className="flex items-center gap-3 rounded-lg border border-border bg-card/60 px-3 py-1.5 font-mono text-xs">
      <div className="relative flex h-3 w-3 items-center justify-center">
        <span
          className={cn("absolute inline-flex h-full w-full rounded-full opacity-50")}
          style={{
            backgroundColor: ringColor,
            animation: !connected || stale ? "none" : `live-pulse-dot ${pulseMs}ms ease-in-out infinite`,
          }}
        />
        <span className={cn("relative h-2 w-2 rounded-full", dotColor)} />
      </div>
      <span className="font-bold tracking-wider">{label}</span>
      <span className="text-muted-foreground">
        {ageMs === Infinity ? "—" : ageMs < 1000 ? `${Math.round(ageMs)}ms` : `${(ageMs / 1000).toFixed(1)}s`}
      </span>
      <span className="text-muted-foreground">{msgsPerSec.toFixed(1)}/s</span>
    </div>
  );
}

function DiagnosticsStrip({
  tape,
  ageMs,
  msgsPerSec,
  tickCount,
  connected,
  stale,
}: {
  tape: TickEvent[];
  ageMs: number;
  msgsPerSec: number;
  tickCount: number;
  connected: boolean;
  stale: boolean;
}) {
  return (
    <div className="flex shrink-0 items-center gap-3 rounded-lg border border-border bg-card/40 px-3 py-2 font-mono text-xs">
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Radio className="h-3 w-3" />
        <span className="text-foreground">ticks</span>
        <span className="tabular-nums text-foreground">{tickCount}</span>
      </div>
      <Divider />
      <Stat label="age" value={ageMs === Infinity ? "—" : ageMs < 1000 ? `${Math.round(ageMs)}ms` : `${(ageMs / 1000).toFixed(1)}s`} />
      <Stat label="rate" value={`${msgsPerSec.toFixed(1)}/s`} />
      <Stat
        label="ws"
        value={!connected ? "down" : stale ? "stale" : "ok"}
        tone={!connected ? "red" : stale ? "amber" : "green"}
      />
      <Divider />
      <div className="flex flex-1 items-center gap-1 overflow-hidden">
        {tape.length === 0 ? (
          <span className="text-muted-foreground">waiting for ticks…</span>
        ) : (
          tape.map((e) => (
            <span
              key={e.id}
              className={cn(
                "shrink-0 rounded border px-1.5 py-0.5 tabular-nums",
                e.dir === "up"
                  ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-300"
                  : e.dir === "down"
                    ? "border-red-500/40 bg-red-500/10 text-red-300"
                    : "border-border text-muted-foreground",
              )}
              style={{
                animation: e.id === tape[0].id ? "live-flash 900ms ease-out" : undefined,
              }}
            >
              {e.kind === "forming" ? "~" : ""}
              {e.price.toFixed(2)}
            </span>
          ))
        )}
      </div>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: "red" | "amber" | "green" }) {
  const color =
    tone === "red" ? "text-red-400" : tone === "amber" ? "text-amber-400" : tone === "green" ? "text-emerald-400" : "text-foreground";
  return (
    <div className="flex items-center gap-1.5">
      <span className="text-muted-foreground">{label}</span>
      <span className={cn("tabular-nums", color)}>{value}</span>
    </div>
  );
}

function Divider() {
  return <span className="h-4 w-px bg-border" />;
}

function Banner({
  tone,
  icon,
  text,
}: {
  tone: "red" | "amber";
  icon: React.ReactNode;
  text: string;
}) {
  const color =
    tone === "red"
      ? "border-red-500/50 bg-red-500/10 text-red-300"
      : "border-amber-500/50 bg-amber-500/10 text-amber-300";
  return (
    <div className={cn("flex shrink-0 items-center gap-2 rounded-md border px-3 py-2 text-sm", color)}>
      {icon}
      <span>{text}</span>
    </div>
  );
}
