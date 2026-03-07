"use client";

import { useServiceHistory } from "@/hooks/use-service-history";
import { relativeTime } from "@/lib/format";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Database,
  Cpu,
  Shield,
  Zap,
  Brain,
  Radio,
  CheckCircle,
  AlertCircle,
  Activity,
  Server,
  HeartPulse,
} from "lucide-react";

// Map service names to icons
const SERVICE_ICONS: Record<string, React.ElementType> = {
  timescaledb: Database,
  ingestion: Cpu,
  monitor: Shield,
  execution: Zap,
  strategy: Brain,
  ws_feed: Radio,
};

export default function ServicesPage() {
  const { serviceHistories, statusChanges, overall, isLoading } = useServiceHistory();

  const ORDER = ["timescaledb", "ingestion", "strategy", "execution", "monitor", "ws_feed"];
  const sortedServices = Array.from(serviceHistories.values()).sort((a, b) => {
    return ORDER.indexOf(a.name) - ORDER.indexOf(b.name);
  });

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Services</h1>
          <p className="text-sm text-muted-foreground">
            System health status and uptime monitoring
          </p>
        </div>
        <Badge
          variant={overall.degraded === 0 ? "default" : "destructive"}
          className="gap-1"
        >
          <div
            className={`h-2 w-2 rounded-full ${
              overall.degraded === 0 ? "bg-emerald-400 animate-pulse" : "bg-red-400"
            }`}
          />
          {overall.degraded === 0 ? "All Systems Operational" : `${overall.degraded} Service(s) Degraded`}
        </Badge>
      </div>

      {/* Summary Stats */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <Server className="h-4 w-4" />
              Total Services
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tabular-nums">{overall.total}</div>
          </CardContent>
        </Card>
        
        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <CheckCircle className="h-4 w-4 text-emerald-500" />
              Healthy
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tabular-nums text-emerald-500">
              {overall.healthy}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <AlertCircle className="h-4 w-4 text-red-500" />
              Degraded
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className={`text-2xl font-bold tabular-nums ${overall.degraded > 0 ? "text-red-500" : "text-muted-foreground"}`}>
              {overall.degraded}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <Activity className="h-4 w-4" />
              Uptime (Session)
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tabular-nums">
              {overall.uptime.toFixed(1)}%
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Service Details */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2 xl:grid-cols-3">
        {isLoading && sortedServices.length === 0 ? (
          <p className="text-muted-foreground col-span-full py-8 text-center">Loading service status...</p>
        ) : (
          sortedServices.map((service) => {
            const Icon = SERVICE_ICONS[service.name] || Server;
            return (
              <Card key={service.name} className={service.currentHealthy ? "" : "border-red-500/50"}>
                <CardHeader className="pb-2">
                  <div className="flex items-start justify-between">
                    <div className="flex items-center gap-2">
                      <div className={`p-2 rounded-md ${service.currentHealthy ? "bg-emerald-500/10 text-emerald-500" : "bg-red-500/10 text-red-500"}`}>
                        <Icon className="h-5 w-5" />
                      </div>
                      <div>
                        <CardTitle className="text-base">{service.name}</CardTitle>
                        <CardDescription className="text-xs">
                          {service.currentHealthy ? "Operational" : "Degraded"}
                        </CardDescription>
                      </div>
                    </div>
                    <Badge variant={service.currentHealthy ? "outline" : "destructive"} className={service.currentHealthy ? "text-emerald-500 border-emerald-500/20" : ""}>
                      {service.currentHealthy ? "Healthy" : "Unhealthy"}
                    </Badge>
                  </div>
                </CardHeader>
                <CardContent className="space-y-4 pt-2">
                  {/* Detail Text */}
                  {service.currentDetail && (
                    <div className="text-xs text-red-400 bg-red-500/10 p-2 rounded border border-red-500/20 font-mono">
                      {service.currentDetail}
                    </div>
                  )}

                  {/* Stats Row */}
                  <div className="flex items-center justify-between text-xs text-muted-foreground">
                    <div className="flex items-center gap-1">
                      <span>Response:</span>
                      <span className="font-mono text-foreground">&lt; 10s</span>
                    </div>
                    <div className="flex items-center gap-1">
                      <span>Uptime:</span>
                      <span className="font-mono text-foreground">{service.uptimePercent.toFixed(1)}%</span>
                    </div>
                  </div>

                  {/* Timeline Dots */}
                  <div className="space-y-1.5">
                    <div className="flex items-center justify-between text-[10px] text-muted-foreground">
                      <span>History (10m)</span>
                      <span>{service.lastChecked ? relativeTime(service.lastChecked.toISOString()) : "—"}</span>
                    </div>
                    <div className="flex items-center gap-0.5 h-3">
                      {service.entries.slice().reverse().map((entry, i) => (
                        <div
                          key={i}
                          className={`h-1.5 w-1.5 rounded-full flex-shrink-0 ${
                            entry.healthy ? "bg-emerald-500" : "bg-red-500"
                          }`}
                          title={entry.timestamp.toLocaleTimeString()}
                        />
                      ))}
                    </div>
                  </div>
                </CardContent>
              </Card>
            );
          })
        )}
      </div>

      {/* Status History Log */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <HeartPulse className="h-5 w-5" />
            Status History
          </CardTitle>
          <CardDescription>Recent status changes detected in this session</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="max-h-64 overflow-y-auto space-y-2">
            {statusChanges.length === 0 ? (
              <p className="text-sm text-muted-foreground py-4 text-center">No status changes recorded.</p>
            ) : (
              statusChanges.map((change, i) => (
                <div key={i} className="flex items-center justify-between text-sm border-b border-border/50 pb-2 last:border-0 last:pb-0">
                  <div className="flex items-center gap-3">
                    <span className="text-muted-foreground text-xs font-mono w-20">
                      {change.timestamp.toLocaleTimeString()}
                    </span>
                    <span className="font-medium">{change.serviceName}</span>
                    <div className="flex items-center gap-2 text-xs">
                      <Badge variant={change.from ? "outline" : "destructive"} className="h-5 px-1.5">
                        {change.from ? "Healthy" : "Degraded"}
                      </Badge>
                      <span className="text-muted-foreground">→</span>
                      <Badge variant={change.to ? "default" : "destructive"} className={`h-5 px-1.5 ${change.to ? "bg-emerald-500 hover:bg-emerald-600" : ""}`}>
                        {change.to ? "Healthy" : "Degraded"}
                      </Badge>
                    </div>
                  </div>
                  {change.detail && (
                    <span className="text-xs text-muted-foreground truncate max-w-[200px]" title={change.detail}>
                      {change.detail}
                    </span>
                  )}
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
