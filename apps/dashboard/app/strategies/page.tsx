"use client";

import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Layers, Pause, Archive, ArrowUpCircle, TrendingUp } from "lucide-react";
import { useState } from "react";
import {
  useStrategyInstances,
  useStrategyList,
  usePromoteInstance,
  useLifecycleAction,
} from "@/hooks/queries";
import type { StrategyInfo } from "@/lib/types";

export default function StrategiesPage() {
  const {
    data: instances = [],
    isLoading: loading,
    error: queryError,
    refetch,
  } = useStrategyInstances();

  const { data: strategies = [] } = useStrategyList();

  const promoteInstance = usePromoteInstance();
  const lifecycleAction = useLifecycleAction();

  const error = queryError ? (queryError instanceof Error ? queryError.message : "Failed to load instances") : null;

  // Track which instance action is in flight
  const [actionLoadingId, setActionLoadingId] = useState<string | null>(null);

  const getNextState = (current: string): string | null => {
    switch (current) {
      case "Draft":
        return "BacktestReady";
      case "BacktestReady":
        return "PaperActive";
      case "PaperActive":
        return "LiveActive";
      case "Deactivated":
        return "PaperActive";
      default:
        return null;
    }
  };

  const handlePromote = async (id: string, currentLifecycle: string) => {
    const target = getNextState(currentLifecycle);
    if (!target) return;

    setActionLoadingId(id);
    try {
      await promoteInstance.mutateAsync({ id, target });
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to promote");
    } finally {
      setActionLoadingId(null);
    }
  };

  const handleLifecycleAction = async (id: string, action: "deactivate" | "archive") => {
    setActionLoadingId(id);
    try {
      await lifecycleAction.mutateAsync({ id, action });
    } catch (err) {
      alert(err instanceof Error ? err.message : `Failed to ${action}`);
    } finally {
      setActionLoadingId(null);
    }
  };

  const getLifecycleBadgeVariant = (lifecycle: string): "default" | "secondary" | "destructive" | "outline" | "ghost" | "link" => {
    switch (lifecycle) {
      case "Draft":
        return "outline";
      case "BacktestReady":
        return "default";
      case "PaperActive":
        return "secondary";
      case "LiveActive":
        return "default";
      case "Deactivated":
        return "destructive";
      case "Archived":
        return "outline";
      default:
        return "outline";
    }
  };

  const getLifecycleBadgeClass = (lifecycle: string) => {
    switch (lifecycle) {
      case "BacktestReady":
        return "bg-blue-500 hover:bg-blue-600 border-transparent text-white";
      case "PaperActive":
        return "bg-amber-500 hover:bg-amber-600 border-transparent text-white";
      case "LiveActive":
        return "bg-emerald-500 hover:bg-emerald-600 border-transparent text-white";
      case "Archived":
        return "text-muted-foreground";
      default:
        return "";
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold text-foreground">
            <Layers className="h-6 w-6" />
            Strategy Instances
          </h1>
          <p className="text-sm text-muted-foreground">
            Manage your strategy instances and their lifecycle states
          </p>
        </div>
        <Button onClick={() => refetch()} variant="outline" size="sm" disabled={loading}>
          Refresh
        </Button>
      </div>

      {/* Strategy Performance Overview */}
      {strategies.length > 0 && (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {strategies.map((strat: StrategyInfo) => (
            <Link key={strat.id} href={`/strategies/${strat.id}`}>
              <Card className="hover:border-emerald-500/50 transition-colors cursor-pointer">
                <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
                  <CardTitle className="text-base font-semibold">{strat.name || strat.id}</CardTitle>
                  <TrendingUp className="h-4 w-4 text-muted-foreground" />
                </CardHeader>
                <CardContent>
                  <div className="flex items-center justify-between">
                    <div className="space-y-1">
                      <p className="text-xs text-muted-foreground">
                        v{strat.version} · Priority {strat.priority}
                      </p>
                      <p className="text-xs font-mono text-muted-foreground">
                        {strat.symbols.join(", ")}
                      </p>
                    </div>
                    <Badge variant={strat.active ? "outline" : "secondary"} className={strat.active ? "border-emerald-500 text-emerald-500" : ""}>
                      {strat.active ? "Active" : "Inactive"}
                    </Badge>
                  </div>
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Instances</CardTitle>
          <CardDescription>
            Overview of all deployed strategy instances
          </CardDescription>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="flex items-center justify-center py-8 text-muted-foreground">
              Loading instances...
            </div>
          ) : error ? (
            <div className="flex items-center justify-center py-8 text-red-400">
              Error: {error}
            </div>
          ) : instances.length === 0 ? (
            <div className="flex items-center justify-center py-8 text-muted-foreground">
              No strategy instances found.
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Instance ID</TableHead>
                  <TableHead>Strategy</TableHead>
                  <TableHead>Lifecycle</TableHead>
                  <TableHead>Symbols</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {instances.map((instance) => (
                  <TableRow key={instance.id}>
                    <TableCell className="font-mono text-xs">{instance.id}</TableCell>
                    <TableCell>{instance.strategyName}</TableCell>
                    <TableCell>
                      <Badge
                        variant={getLifecycleBadgeVariant(instance.lifecycle)}
                        className={getLifecycleBadgeClass(instance.lifecycle)}
                      >
                        {instance.lifecycle}
                      </Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {instance.symbols.join(", ")}
                    </TableCell>
                    <TableCell>
                      {instance.isActive ? (
                        <Badge variant="outline" className="border-emerald-500 text-emerald-500 gap-1">
                          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
                          Active
                        </Badge>
                      ) : (
                        <Badge variant="outline" className="text-muted-foreground">
                          Inactive
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-2">
                        {/* Promote Button */}
                        {instance.allowedTransitions?.some(t => 
                          ["BacktestReady", "PaperActive", "LiveActive"].includes(t) && t !== instance.lifecycle
                        ) && (
                          <Button
                            variant="outline"
                            size="xs"
                            onClick={() => handlePromote(instance.id, instance.lifecycle)}
                            disabled={actionLoadingId === instance.id}
                            className="gap-1"
                          >
                            <ArrowUpCircle className="h-3 w-3" />
                            Promote
                          </Button>
                        )}

                        {/* Deactivate Button */}
                        {instance.allowedTransitions?.includes("Deactivated") && (
                          <Button
                            variant="outline"
                            size="xs"
                            onClick={() => handleLifecycleAction(instance.id, "deactivate")}
                            disabled={actionLoadingId === instance.id}
                            className="gap-1 border-red-200 hover:bg-red-50 hover:text-red-600 dark:border-red-900 dark:hover:bg-red-900/20"
                          >
                            <Pause className="h-3 w-3" />
                            Deactivate
                          </Button>
                        )}

                        {/* Archive Button */}
                        {instance.allowedTransitions?.includes("Archived") && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => handleLifecycleAction(instance.id, "archive")}
                            disabled={actionLoadingId === instance.id}
                            className="gap-1 text-muted-foreground hover:text-foreground"
                          >
                            <Archive className="h-3 w-3" />
                            Archive
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
