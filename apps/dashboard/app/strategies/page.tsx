"use client";

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
import { Layers, Play, Pause, Archive, ArrowUpCircle } from "lucide-react";
import { useEffect, useState } from "react";

interface InstanceInfo {
  id: string;
  strategyName: string;
  lifecycle: string; // "Draft" | "BacktestReady" | "PaperActive" | "LiveActive" | "Deactivated" | "Archived"
  symbols: string[];
  isActive: boolean;
  allowedTransitions: string[];
}

export default function StrategiesPage() {
  const [instances, setInstances] = useState<InstanceInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionLoading, setActionLoading] = useState<string | null>(null);

  const fetchInstances = async () => {
    try {
      setLoading(true);
      const res = await fetch("/api/strategies/v2/instances");
      if (!res.ok) {
        throw new Error(`Failed to fetch instances: ${res.statusText}`);
      }
      const data = await res.json();
      setInstances(data || []);
      setError(null);
    } catch (err) {
      console.error(err);
      setError(err instanceof Error ? err.message : "Failed to load instances");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchInstances();
  }, []);

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

    setActionLoading(id);
    try {
      const res = await fetch(`/api/strategies/v2/instances/${encodeURIComponent(id)}/promote`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ target }),
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || "Failed to promote instance");
      }
      await fetchInstances();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to promote");
    } finally {
      setActionLoading(null);
    }
  };

  const handleLifecycleAction = async (id: string, action: "deactivate" | "archive") => {
    setActionLoading(id);
    try {
      const res = await fetch(`/api/strategies/v2/instances/${encodeURIComponent(id)}/${action}`, {
        method: "POST",
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || `Failed to ${action} instance`);
      }
      await fetchInstances();
    } catch (err) {
      alert(err instanceof Error ? err.message : `Failed to ${action}`);
    } finally {
      setActionLoading(null);
    }
  };

  const getLifecycleBadgeVariant = (lifecycle: string): "default" | "secondary" | "destructive" | "outline" | "ghost" | "link" => {
    switch (lifecycle) {
      case "Draft":
        return "outline";
      case "BacktestReady":
        return "default"; // blue-ish in default theme usually, but let's check class usage
      case "PaperActive":
        return "secondary"; // often yellow/amber in some themes or gray, will adjust with class
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
        <Button onClick={fetchInstances} variant="outline" size="sm" disabled={loading}>
          Refresh
        </Button>
      </div>

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
                            disabled={actionLoading === instance.id}
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
                            disabled={actionLoading === instance.id}
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
                            disabled={actionLoading === instance.id}
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
