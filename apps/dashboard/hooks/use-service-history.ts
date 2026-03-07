import { useRef, useState, useEffect } from "react";
import { useServiceHealth } from "./queries";

export interface ServiceHistoryEntry {
  timestamp: Date;
  healthy: boolean;
  detail?: string;
}

export interface StatusChangeEvent {
  timestamp: Date;
  serviceName: string;
  from: boolean;
  to: boolean;
  detail?: string;
}

export interface ServiceHistory {
  name: string;
  entries: ServiceHistoryEntry[];
  uptimePercent: number;
  lastChecked: Date | null;
  currentHealthy: boolean;
  currentDetail?: string;
}

const MAX_HISTORY_ENTRIES = 60; // 10 minutes at 10s interval

export function useServiceHistory() {
  const { data: healthData, isLoading } = useServiceHealth();
  
  // Ref to store history across renders without causing re-renders itself
  // We use state to expose the data to the component
  const historyRef = useRef<Map<string, ServiceHistoryEntry[]>>(new Map());
  const systemHistoryRef = useRef<boolean[]>([]);
  const prevStatusesRef = useRef<Map<string, boolean>>(new Map());

  const [serviceHistories, setServiceHistories] = useState<Map<string, ServiceHistory>>(new Map());
  const [statusChanges, setStatusChanges] = useState<StatusChangeEvent[]>([]);
  const [systemStats, setSystemStats] = useState({
    total: 0,
    healthy: 0,
    degraded: 0,
    uptime: 100,
  });

  useEffect(() => {
    if (!healthData?.services) return;

    const now = new Date();
    const newChanges: StatusChangeEvent[] = [];
    const newHistories = new Map<string, ServiceHistory>();
    
    // Track system health for this tick
    const allHealthy = healthData.services.every(s => s.healthy);
    systemHistoryRef.current.push(allHealthy);

    // Calculate system uptime based on all ticks seen so far
    const totalTicks = systemHistoryRef.current.length;
    const healthyTicks = systemHistoryRef.current.filter(h => h).length;
    const currentSystemUptime = totalTicks > 0 ? (healthyTicks / totalTicks) * 100 : 100;

    healthData.services.forEach((service) => {
      // 1. Update history entries
      const currentEntries = historyRef.current.get(service.name) || [];
      const newEntry: ServiceHistoryEntry = {
        timestamp: now,
        healthy: service.healthy,
        detail: service.detail,
      };
      
      // Keep last N entries
      const updatedEntries = [newEntry, ...currentEntries].slice(0, MAX_HISTORY_ENTRIES);
      historyRef.current.set(service.name, updatedEntries);

      // 2. Check for status changes
      const prevHealthy = prevStatusesRef.current.get(service.name);
      if (prevHealthy !== undefined && prevHealthy !== service.healthy) {
        newChanges.push({
          timestamp: now,
          serviceName: service.name,
          from: prevHealthy,
          to: service.healthy,
          detail: service.detail,
        });
      }
      prevStatusesRef.current.set(service.name, service.healthy);

      // 3. Calculate service uptime (simple % of visible history)
      const serviceHealthyTicks = updatedEntries.filter(e => e.healthy).length;
      const serviceUptime = updatedEntries.length > 0 
        ? (serviceHealthyTicks / updatedEntries.length) * 100 
        : 100;

      newHistories.set(service.name, {
        name: service.name,
        entries: updatedEntries,
        uptimePercent: serviceUptime,
        lastChecked: now,
        currentHealthy: service.healthy,
        currentDetail: service.detail,
      });
    });

    // Update states
    setServiceHistories(newHistories);
    
    if (newChanges.length > 0) {
      setStatusChanges(prev => [...newChanges, ...prev].slice(0, 50));
    }

    setSystemStats({
      total: healthData.services.length,
      healthy: healthData.services.filter(s => s.healthy).length,
      degraded: healthData.services.filter(s => !s.healthy).length,
      uptime: currentSystemUptime,
    });

  }, [healthData]);

  return {
    serviceHistories,
    statusChanges,
    overall: systemStats,
    lastUpdated: healthData ? new Date() : null,
    isLoading
  };
}
