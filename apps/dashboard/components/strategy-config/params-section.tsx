"use client";

import type { ParamMeta } from "@/lib/use-strategy-config";
import { ParamField } from "./param-field";

interface ParamsSectionProps {
  params: Record<string, unknown>;
  schema: ParamMeta[];
  group: string;
  onChange: (key: string, value: unknown) => void;
}

export function ParamsSection({ params, schema, group, onChange }: ParamsSectionProps) {
  const fields = schema.filter((m) => m.group === group);
  if (fields.length === 0) return null;

  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold text-zinc-300">{group}</h3>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        {fields.map((meta) => (
          <ParamField key={meta.key} meta={meta} value={params[meta.key]} onChange={onChange} />
        ))}
      </div>
    </div>
  );
}
