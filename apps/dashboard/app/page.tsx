import { MarketingNav } from "@/components/landing/marketing-nav";
import { Hero } from "@/components/landing/hero";
import { LiveStrip } from "@/components/landing/live-strip";
import { CapabilityGrid } from "@/components/landing/capability-grid";
import { EdgeThesis } from "@/components/landing/edge-thesis";
import { AIEdge } from "@/components/landing/ai-edge";
import { StrategyFamilies } from "@/components/landing/strategy-families";
import { ArchitectureHex } from "@/components/landing/architecture-hex";
import { ReliabilitySection } from "@/components/landing/reliability-section";
import { BacktestPerformance } from "@/components/landing/backtest-performance";
import { OperatorFeed } from "@/components/landing/operator-feed";
import { RoadmapTimeline } from "@/components/landing/roadmap-timeline";
import { LimitationsSection } from "@/components/landing/limitations-section";
import { MinimalFooter } from "@/components/landing/minimal-footer";

export default function LandingPage() {
  return (
    <div className="landing-root min-h-screen">
      <MarketingNav />
      <Hero />
      <LiveStrip />
      <CapabilityGrid />
      <EdgeThesis />
      <AIEdge />
      <StrategyFamilies />
      <ArchitectureHex />
      <ReliabilitySection />
      <BacktestPerformance />
      <OperatorFeed />
      <RoadmapTimeline />
      <LimitationsSection />
      <MinimalFooter />
    </div>
  );
}
