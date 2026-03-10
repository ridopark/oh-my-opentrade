"use client";

import { Scale } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { StrategyRow } from "@/lib/types";
import { cn } from "@/lib/utils";

interface StrategyComparisonTableProps {
  data: StrategyRow[] | undefined;
}

const formatCurrency = (value: number) => {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(value);
};

const formatPercent = (value: number) => {
  return `${(value * 100).toFixed(2)}%`;
};

export function StrategyComparisonTable({ data }: StrategyComparisonTableProps) {
  if (!data || data.length === 0) {
    return (
      <Card className="col-span-1 lg:col-span-2">
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-sm font-medium">Strategy Comparison</CardTitle>
          <Scale className="h-4 w-4 text-muted-foreground" />
        </CardHeader>
        <CardContent>
          <div className="flex h-[200px] items-center justify-center text-sm text-slate-500">
            No strategy data for this period
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="col-span-1 lg:col-span-2">
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <CardTitle className="text-sm font-medium">Strategy Comparison</CardTitle>
        <Scale className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className="rounded-md border border-slate-800">
          <Table>
            <TableHeader>
              <TableRow className="border-slate-800 hover:bg-transparent">
                <TableHead>Strategy</TableHead>
                <TableHead className="text-right">P&L</TableHead>
                <TableHead className="text-right">Fees</TableHead>
                <TableHead className="text-right">Trades</TableHead>
                <TableHead className="text-right">Wins</TableHead>
                <TableHead className="text-right">Losses</TableHead>
                <TableHead className="text-right">Win Rate</TableHead>
                <TableHead className="text-right">Profit Factor</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map((row) => (
                <TableRow key={row.strategy} className="border-slate-800">
                  <TableCell className="font-medium">{row.strategy}</TableCell>
                  <TableCell className={cn(
                    "text-right font-medium",
                    row.realized_pnl >= 0 ? "text-emerald-500" : "text-rose-500"
                  )}>
                    {formatCurrency(row.realized_pnl)}
                  </TableCell>
                  <TableCell className="text-right text-slate-400">
                    {formatCurrency(row.fees)}
                  </TableCell>
                  <TableCell className="text-right">{row.total_trades}</TableCell>
                  <TableCell className="text-right text-emerald-500">{row.win_count}</TableCell>
                  <TableCell className="text-right text-rose-500">{row.loss_count}</TableCell>
                  <TableCell className="text-right">
                    {row.win_rate !== null ? formatPercent(row.win_rate) : "—"}
                  </TableCell>
                  <TableCell className="text-right">
                    {row.profit_factor !== null ? row.profit_factor.toFixed(2) : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}
