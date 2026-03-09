import { describe, it, expect } from "vitest";
import { queryKeys } from "@/hooks/queries";

describe("queryKeys", () => {
  describe("static keys", () => {
    it("health key is a stable readonly tuple", () => {
      expect(queryKeys.health).toEqual(["health", "services"]);
      expect(queryKeys.health).toBe(queryKeys.health);
    });

    it("strategyInstances key", () => {
      expect(queryKeys.strategyInstances).toEqual([
        "strategies",
        "instances",
      ]);
    });

    it("currentStrategy key", () => {
      expect(queryKeys.currentStrategy).toEqual(["strategies", "current"]);
    });

    it("allStrategiesDNA key", () => {
      expect(queryKeys.allStrategiesDNA).toEqual([
        "strategies",
        "dna",
        "all",
      ]);
    });

    it("strategyList key", () => {
      expect(queryKeys.strategyList).toEqual(["strategies", "perf", "list"]);
    });
  });

  describe("parameterized key factories", () => {
    it("performanceDashboard includes the range parameter", () => {
      expect(queryKeys.performanceDashboard("7d")).toEqual([
        "performance",
        "dashboard",
        "7d",
      ]);
      expect(queryKeys.performanceDashboard("30d")).toEqual([
        "performance",
        "dashboard",
        "30d",
      ]);
    });

    it("performanceTrades includes the range parameter", () => {
      expect(queryKeys.performanceTrades("7d")).toEqual([
        "performance",
        "trades",
        "7d",
      ]);
    });

    it("strategyDashboard includes strategy ID and range", () => {
      expect(queryKeys.strategyDashboard("momentum-v2", "30d")).toEqual([
        "strategies",
        "perf",
        "momentum-v2",
        "dashboard",
        "30d",
      ]);
    });

    it("strategyState includes strategy ID", () => {
      expect(queryKeys.strategyState("mean-reversion")).toEqual([
        "strategies",
        "perf",
        "mean-reversion",
        "state",
      ]);
    });

    it("strategySignals includes strategy ID", () => {
      expect(queryKeys.strategySignals("breakout-v1")).toEqual([
        "strategies",
        "perf",
        "breakout-v1",
        "signals",
      ]);
    });
  });

  describe("key uniqueness for cache isolation", () => {
    it("different ranges produce different keys", () => {
      const key7d = queryKeys.performanceDashboard("7d");
      const key30d = queryKeys.performanceDashboard("30d");
      expect(key7d).not.toEqual(key30d);
    });

    it("different strategies produce different keys", () => {
      const keyA = queryKeys.strategyDashboard("alpha", "7d");
      const keyB = queryKeys.strategyDashboard("beta", "7d");
      expect(keyA).not.toEqual(keyB);
    });

    it("same strategy with different ranges produces different keys", () => {
      const key7d = queryKeys.strategyDashboard("alpha", "7d");
      const key30d = queryKeys.strategyDashboard("alpha", "30d");
      expect(key7d).not.toEqual(key30d);
    });

    it("factory calls return fresh arrays (not shared references)", () => {
      const a = queryKeys.performanceDashboard("7d");
      const b = queryKeys.performanceDashboard("7d");
      expect(a).toEqual(b);
      expect(a).not.toBe(b);
    });
  });
});
