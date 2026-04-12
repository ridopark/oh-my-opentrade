export interface RollingDecayPoint {
  tradeSeq: number;
  pnl: number;
  rollingPf20: number | null;
  rollingPf60: number | null;
  rollingPf120: number | null;
  rollingWr60: number | null;
}

export interface ComponentAttribution {
  component: string;
  group: string;
  nFired: number;
  nNotFired: number;
  pfFired: number | null;
  pfNotFired: number | null;
  marginal: number | null;
}
