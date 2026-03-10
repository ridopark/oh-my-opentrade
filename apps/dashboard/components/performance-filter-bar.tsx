"use client";

import * as React from "react";

import { PerformanceFilters } from "@/hooks/queries";
import { Button } from "@/components/ui/button";
import { DateRangePicker } from "@/components/ui/date-range-picker";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

interface PerformanceFilterBarProps {
  filters: PerformanceFilters;
  onFiltersChange: (filters: PerformanceFilters) => void;
  strategies?: string[];
  symbols?: string[];
  className?: string;
}

const RANGE_PRESETS = [
  { label: "7D", value: "7d" },
  { label: "30D", value: "30d" },
  { label: "90D", value: "90d" },
  { label: "ALL", value: "all" },
];

export function PerformanceFilterBar({
  filters,
  onFiltersChange,
  strategies = [],
  symbols = [],
  className,
}: PerformanceFilterBarProps) {
  const handleRangePreset = (range: string) => {
    onFiltersChange({
      ...filters,
      range,
      from: undefined,
      to: undefined,
    });
  };

  const handleDateRange = (range: { from?: Date; to?: Date }) => {
    // Only update if we have both dates, or if we cleared the selection
    if (range.from && range.to) {
      onFiltersChange({
        ...filters,
        from: range.from.toISOString(),
        to: range.to.toISOString(),
        range: undefined,
      });
    } else if (!range.from && !range.to) {
      // Revert to default 30d if cleared
      onFiltersChange({
        ...filters,
        from: undefined,
        to: undefined,
        range: "30d",
      });
    }
  };

  const handleStrategyChange = (strategy: string) => {
    onFiltersChange({
      ...filters,
      strategy: strategy === "all" ? undefined : strategy,
    });
  };

  const handleSymbolChange = (symbol: string) => {
    onFiltersChange({
      ...filters,
      symbol: symbol === "all" ? undefined : symbol,
    });
  };

  // Parse current from/to strings into Date objects for the picker
  const fromDate = filters.from ? new Date(filters.from) : undefined;
  const toDate = filters.to ? new Date(filters.to) : undefined;

  return (
    <div className={cn("flex flex-col sm:flex-row flex-wrap gap-4 items-start sm:items-center", className)}>
      {/* Range Presets */}
      <div className="flex bg-muted p-1 rounded-md">
        {RANGE_PRESETS.map((preset) => {
          const isActive = filters.range === preset.value && !filters.from;
          return (
            <Button
              key={preset.value}
              variant={isActive ? "default" : "ghost"}
              size="sm"
              onClick={() => handleRangePreset(preset.value)}
              className={cn(
                "px-4 py-1 h-8",
                isActive ? "shadow-sm" : "hover:bg-transparent hover:text-foreground text-muted-foreground"
              )}
            >
              {preset.label}
            </Button>
          );
        })}
      </div>

      {/* Custom Date Range Picker */}
      <DateRangePicker
        from={fromDate}
        to={toDate}
        onChange={handleDateRange}
        className="w-[260px]"
      />

      {/* Optional Strategy Filter */}
      {strategies.length > 0 && (
        <Select
          value={filters.strategy || "all"}
          onValueChange={handleStrategyChange}
        >
          <SelectTrigger className="w-[180px]">
            <SelectValue placeholder="All Strategies" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Strategies</SelectItem>
            {strategies.map((strat) => (
              <SelectItem key={strat} value={strat}>
                {strat}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}

      {/* Optional Symbol Filter */}
      {symbols.length > 0 && (
        <Select value={filters.symbol || "all"} onValueChange={handleSymbolChange}>
          <SelectTrigger className="w-[160px]">
            <SelectValue placeholder="All Symbols" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Symbols</SelectItem>
            {symbols.map((sym) => (
              <SelectItem key={sym} value={sym}>
                {sym}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    </div>
  );
}
