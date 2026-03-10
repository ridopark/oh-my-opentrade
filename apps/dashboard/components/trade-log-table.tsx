"use client";

import React, { useState } from "react";
import {
  useReactTable,
  getCoreRowModel,
  getSortedRowModel,
  getExpandedRowModel,
  flexRender,
  ColumnDef,
  SortingState,
  ExpandedState,
} from "@tanstack/react-table";
import {
  ChevronDown,
  ChevronRight,
  ArrowUpDown,
  ArrowUp,
  ArrowDown,
} from "lucide-react";

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { TradeEntry } from "@/lib/types";

// Helper for formatting currency
const formatCurrency = (val: number) =>
  new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(val);

interface TradeLogTableProps {
  trades: TradeEntry[];
  hasNextPage?: boolean;
  isFetchingNextPage?: boolean;
  onLoadMore?: () => void;
}

export function TradeLogTable({
  trades,
  hasNextPage,
  isFetchingNextPage,
  onLoadMore,
}: TradeLogTableProps) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const [expanded, setExpanded] = useState<ExpandedState>({});

  const columns: ColumnDef<TradeEntry>[] = [
    {
      id: "expander",
      header: () => null,
      cell: ({ row }) => {
        return row.getCanExpand() ? (
          <button
            onClick={row.getToggleExpandedHandler()}
            className="cursor-pointer"
          >
            {row.getIsExpanded() ? (
              <ChevronDown className="h-4 w-4" />
            ) : (
              <ChevronRight className="h-4 w-4" />
            )}
          </button>
        ) : null;
      },
    },
    {
      accessorKey: "time",
      header: ({ column }) => {
        return (
          <Button
            variant="ghost"
            onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
            className="-ml-4 h-8 px-4 font-medium"
          >
            Time
            {column.getIsSorted() === "asc" ? (
              <ArrowUp className="ml-2 h-4 w-4" />
            ) : column.getIsSorted() === "desc" ? (
              <ArrowDown className="ml-2 h-4 w-4" />
            ) : (
              <ArrowUpDown className="ml-2 h-4 w-4" />
            )}
          </Button>
        );
      },
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {new Date(row.original.time).toLocaleString()}
        </span>
      ),
    },
    {
      accessorKey: "symbol",
      header: ({ column }) => {
        return (
          <Button
            variant="ghost"
            onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
            className="-ml-4 h-8 px-4 font-medium"
          >
            Symbol
            {column.getIsSorted() === "asc" ? (
              <ArrowUp className="ml-2 h-4 w-4" />
            ) : column.getIsSorted() === "desc" ? (
              <ArrowDown className="ml-2 h-4 w-4" />
            ) : (
              <ArrowUpDown className="ml-2 h-4 w-4" />
            )}
          </Button>
        );
      },
      cell: ({ row }) => (
        <span className="font-medium">{row.original.symbol}</span>
      ),
    },
    {
      accessorKey: "side",
      header: ({ column }) => {
        return (
          <Button
            variant="ghost"
            onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
            className="-ml-4 h-8 px-4 font-medium"
          >
            Side
            {column.getIsSorted() === "asc" ? (
              <ArrowUp className="ml-2 h-4 w-4" />
            ) : column.getIsSorted() === "desc" ? (
              <ArrowDown className="ml-2 h-4 w-4" />
            ) : (
              <ArrowUpDown className="ml-2 h-4 w-4" />
            )}
          </Button>
        );
      },
      cell: ({ row }) => (
        <span
          className={cn(
            "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
            row.original.side === "BUY"
              ? "bg-emerald-500/10 text-emerald-500"
              : "bg-rose-500/10 text-rose-500"
          )}
        >
          {row.original.side}
        </span>
      ),
    },
    {
      accessorKey: "quantity",
      header: ({ column }) => {
        return (
          <div className="flex justify-end">
            <Button
              variant="ghost"
              onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
              className="-mr-4 h-8 px-4 font-medium"
            >
              Qty
              {column.getIsSorted() === "asc" ? (
                <ArrowUp className="ml-2 h-4 w-4" />
              ) : column.getIsSorted() === "desc" ? (
                <ArrowDown className="ml-2 h-4 w-4" />
              ) : (
                <ArrowUpDown className="ml-2 h-4 w-4" />
              )}
            </Button>
          </div>
        );
      },
      cell: ({ row }) => (
        <div className="text-right font-mono">{row.original.quantity}</div>
      ),
    },
    {
      accessorKey: "price",
      header: ({ column }) => {
        return (
          <div className="flex justify-end">
            <Button
              variant="ghost"
              onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
              className="-mr-4 h-8 px-4 font-medium"
            >
              Price
              {column.getIsSorted() === "asc" ? (
                <ArrowUp className="ml-2 h-4 w-4" />
              ) : column.getIsSorted() === "desc" ? (
                <ArrowDown className="ml-2 h-4 w-4" />
              ) : (
                <ArrowUpDown className="ml-2 h-4 w-4" />
              )}
            </Button>
          </div>
        );
      },
      cell: ({ row }) => (
        <div className="text-right font-mono">
          {formatCurrency(row.original.price)}
        </div>
      ),
    },
    {
      accessorKey: "commission",
      header: () => <div className="text-right">Comm</div>,
      cell: ({ row }) => (
        <div className="text-right font-mono text-muted-foreground">
          {formatCurrency(row.original.commission)}
        </div>
      ),
    },
    {
      accessorKey: "status",
      header: "Status",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground uppercase">
          {row.original.status}
        </span>
      ),
    },
  ];

  const table = useReactTable({
    data: trades,
    columns,
    state: {
      sorting,
      expanded,
    },
    onSortingChange: setSorting,
    onExpandedChange: setExpanded,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getExpandedRowModel: getExpandedRowModel(),
    getRowCanExpand: () => true,
  });

  if (trades.length === 0 && !isFetchingNextPage) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Trades</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="text-center text-muted-foreground py-8">
            No trades in this period
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Trades</CardTitle>
      </CardHeader>
      <CardContent className="p-0 sm:p-6">
        <div className="space-y-4">
          <div className="rounded-md border">
            <Table>
              <TableHeader>
                {table.getHeaderGroups().map((headerGroup) => (
                  <TableRow key={headerGroup.id}>
                    {headerGroup.headers.map((header) => {
                      return (
                        <TableHead key={header.id}>
                          {header.isPlaceholder
                            ? null
                            : flexRender(
                                header.column.columnDef.header,
                                header.getContext()
                              )}
                        </TableHead>
                      );
                    })}
                  </TableRow>
                ))}
              </TableHeader>
              <TableBody>
                {table.getRowModel().rows?.length ? (
                  table.getRowModel().rows.map((row) => (
                    <React.Fragment key={row.id}>
                      <TableRow data-state={row.getIsSelected() && "selected"}>
                        {row.getVisibleCells().map((cell) => (
                          <TableCell key={cell.id}>
                            {flexRender(
                              cell.column.columnDef.cell,
                              cell.getContext()
                            )}
                          </TableCell>
                        ))}
                      </TableRow>
                      {row.getIsExpanded() && (
                        <TableRow className="bg-muted/5">
                          <TableCell
                            colSpan={row.getVisibleCells().length}
                            className="border-l-2 border-emerald-500/20 py-3"
                          >
                            <div className="flex flex-col gap-1 pl-6">
                              <div className="text-xs text-muted-foreground">
                                <span className="font-semibold text-foreground mr-2">
                                  Trade ID:
                                </span>
                                <span className="font-mono">
                                  {row.original.trade_id}
                                </span>
                              </div>
                              {/* Strategy check as requested */}
                              {"strategy" in row.original && (row.original as TradeEntry & { strategy?: string }).strategy && (
                                <div className="text-xs text-muted-foreground">
                                  <span className="font-semibold text-foreground mr-2">
                                    Strategy:
                                  </span>
                                  <span>{(row.original as TradeEntry & { strategy?: string }).strategy}</span>
                                </div>
                              )}
                              {"rationale" in row.original && (row.original as TradeEntry & { rationale?: string }).rationale && (
                                <div className="text-xs text-muted-foreground">
                                  <span className="font-semibold text-foreground mr-2">
                                    Rationale:
                                  </span>
                                  <span>{(row.original as TradeEntry & { rationale?: string }).rationale}</span>
                                </div>
                              )}
                              <div className="text-xs text-muted-foreground">
                                <span className="font-semibold text-foreground mr-2">
                                  Timestamp:
                                </span>
                                <span className="font-mono">
                                  {row.original.time}
                                </span>
                              </div>
                            </div>
                          </TableCell>
                        </TableRow>
                      )}
                    </React.Fragment>
                  ))
                ) : (
                  <TableRow>
                    <TableCell
                      colSpan={columns.length}
                      className="h-24 text-center"
                    >
                      No trades.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>

          {hasNextPage && (
            <div className="flex justify-center pt-4">
              <Button
                variant="outline"
                onClick={onLoadMore}
                disabled={isFetchingNextPage}
              >
                {isFetchingNextPage ? "Loading..." : "Load More"}
              </Button>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
