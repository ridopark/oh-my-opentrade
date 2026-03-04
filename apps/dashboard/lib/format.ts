export function relativeTime(isoString: string): string {
  const now = Date.now();
  const then = new Date(isoString).getTime();
  const diffMs = now - then;

  if (diffMs < 0) return "just now";

  const seconds = Math.floor(diffMs / 1000);
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;

  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;

  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;

  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

export function formatPrice(price: number): string {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
  }).format(price);
}

export function formatPercent(value: number): string {
  return `${(value * 100).toFixed(1)}%`;
}

export function formatNumber(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return value.toFixed(0);
}

export function isMarketOpen(): boolean {
  const now = new Date();
  const eastern = new Date(
    now.toLocaleString("en-US", { timeZone: "America/New_York" })
  );
  const day = eastern.getDay();
  const hours = eastern.getHours();
  const minutes = eastern.getMinutes();
  const time = hours * 60 + minutes;

  // Market hours: Mon-Fri, 9:30 AM - 4:00 PM ET
  if (day === 0 || day === 6) return false;
  return time >= 570 && time < 960; // 9:30=570, 16:00=960
}
