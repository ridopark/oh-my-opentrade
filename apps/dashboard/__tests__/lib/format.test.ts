import { describe, it, expect, vi, afterEach } from "vitest";
import {
  relativeTime,
  formatPrice,
  formatPercent,
  formatNumber,
  isCryptoSymbol,
} from "@/lib/format";

describe("relativeTime", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('returns "just now" for timestamps less than 10 seconds ago', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2025-01-15T12:00:05Z"));
    expect(relativeTime("2025-01-15T12:00:00Z")).toBe("just now");
  });

  it("returns seconds ago for timestamps between 10-59 seconds ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2025-01-15T12:00:30Z"));
    expect(relativeTime("2025-01-15T12:00:00Z")).toBe("30s ago");
  });

  it("returns minutes ago for timestamps between 1-59 minutes ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2025-01-15T12:15:00Z"));
    expect(relativeTime("2025-01-15T12:00:00Z")).toBe("15 min ago");
  });

  it("returns hours ago for timestamps between 1-23 hours ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2025-01-15T15:00:00Z"));
    expect(relativeTime("2025-01-15T12:00:00Z")).toBe("3h ago");
  });

  it("returns days ago for timestamps 24+ hours ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2025-01-18T12:00:00Z"));
    expect(relativeTime("2025-01-15T12:00:00Z")).toBe("3d ago");
  });

  it('returns "just now" for future timestamps', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2025-01-15T12:00:00Z"));
    expect(relativeTime("2025-01-15T12:05:00Z")).toBe("just now");
  });
});

describe("formatPrice", () => {
  it("formats a whole number as USD currency with two decimals", () => {
    expect(formatPrice(100)).toBe("$100.00");
  });

  it("formats decimal prices correctly", () => {
    expect(formatPrice(1234.5)).toBe("$1,234.50");
  });

  it("formats zero", () => {
    expect(formatPrice(0)).toBe("$0.00");
  });

  it("formats negative prices", () => {
    expect(formatPrice(-50.99)).toBe("-$50.99");
  });

  it("formats large prices with comma separators", () => {
    expect(formatPrice(1000000)).toBe("$1,000,000.00");
  });
});

describe("formatPercent", () => {
  it("converts decimal ratio to percentage string with one decimal", () => {
    expect(formatPercent(0.756)).toBe("75.6%");
  });

  it("handles zero", () => {
    expect(formatPercent(0)).toBe("0.0%");
  });

  it("handles 100% (ratio of 1.0)", () => {
    expect(formatPercent(1)).toBe("100.0%");
  });

  it("handles negative values", () => {
    expect(formatPercent(-0.125)).toBe("-12.5%");
  });
});

describe("formatNumber", () => {
  it("formats millions with M suffix", () => {
    expect(formatNumber(2_500_000)).toBe("2.5M");
  });

  it("formats thousands with K suffix", () => {
    expect(formatNumber(15_700)).toBe("15.7K");
  });

  it("formats small numbers without suffix", () => {
    expect(formatNumber(42)).toBe("42");
  });

  it("formats exactly 1 million", () => {
    expect(formatNumber(1_000_000)).toBe("1.0M");
  });

  it("formats exactly 1 thousand", () => {
    expect(formatNumber(1_000)).toBe("1.0K");
  });

  it("rounds small numbers to integers", () => {
    expect(formatNumber(999.7)).toBe("1000");
  });
});

describe("isCryptoSymbol", () => {
  it("returns true for crypto pairs containing /", () => {
    expect(isCryptoSymbol("BTC/USD")).toBe(true);
    expect(isCryptoSymbol("ETH/USDT")).toBe(true);
  });

  it("returns false for stock ticker symbols", () => {
    expect(isCryptoSymbol("AAPL")).toBe(false);
    expect(isCryptoSymbol("TSLA")).toBe(false);
    expect(isCryptoSymbol("SPY")).toBe(false);
  });
});
