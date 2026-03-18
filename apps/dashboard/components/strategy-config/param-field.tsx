"use client";

import type { ParamMeta } from "@/lib/use-strategy-config";

const inputCls =
  "bg-background border border-border rounded px-2 py-1.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500 w-full";

function formatLabel(key: string): string {
  const base = key.includes(".") ? key.split(".").pop()! : key;
  return base.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

interface ParamFieldProps {
  meta: ParamMeta;
  value: unknown;
  onChange: (key: string, value: unknown) => void;
}

export function ParamField({ meta, value, onChange }: ParamFieldProps) {
  const handleNumber = (e: React.ChangeEvent<HTMLInputElement>) => {
    const raw = e.target.value;
    if (raw === "") return;
    onChange(meta.key, meta.type === "integer" ? parseInt(raw, 10) : parseFloat(raw));
  };

  return (
    <div className="space-y-1">
      <label className="flex items-center gap-2 text-xs text-muted-foreground" title={meta.description}>
        <span>{formatLabel(meta.key)}</span>
        {meta.default !== undefined && (
          <span className="text-[10px] text-zinc-600">({String(meta.default)})</span>
        )}
      </label>

      {(meta.type === "integer" || meta.type === "number") && (
        <input
          type="number"
          className={inputCls}
          value={value as number ?? ""}
          onChange={handleNumber}
          min={meta.min}
          max={meta.max}
          step={meta.step ?? (meta.type === "integer" ? 1 : 0.1)}
        />
      )}

      {meta.type === "boolean" && (
        <input
          type="checkbox"
          checked={!!value}
          onChange={(e) => onChange(meta.key, e.target.checked)}
          className="h-4 w-4 rounded border-border bg-background"
        />
      )}

      {meta.type === "string" && (
        <input
          type="text"
          className={inputCls}
          value={(value as string) ?? ""}
          onChange={(e) => onChange(meta.key, e.target.value)}
        />
      )}

      {meta.type === "string_array" && (
        <input
          type="text"
          className={inputCls}
          value={Array.isArray(value) ? (value as string[]).join(", ") : String(value ?? "")}
          onChange={(e) => onChange(meta.key, e.target.value.split(",").map((s) => s.trim()).filter(Boolean))}
        />
      )}
    </div>
  );
}
