package ports

import "time"

// PipelineHealthReporter provides pipeline liveness timestamps.
// Implemented by the ingestion service; consumed by feed watchdogs
// to detect deadlocks where bars arrive from the network but stall
// in the processing pipeline.
type PipelineHealthReporter interface {
	// LastProcessedAt returns when the pipeline last successfully processed
	// a bar for the given feed type ("equity" or "crypto").
	// Returns zero time if no bars have been processed yet for that feed.
	LastProcessedAt(feedType string) time.Time

	// LastProcessedAtSymbol returns when the pipeline last successfully
	// processed a bar for the given symbol. Implementations may track
	// per-symbol timestamps directly or fall back to the feed-level
	// timestamp corresponding to the symbol's asset class. Returns the
	// zero time when neither a per-symbol nor a feed-level timestamp is
	// available. Phase 3 of the strategy-liveness plan added this method
	// so the dashboard can render per-symbol freshness without the client
	// having to resolve symbol->feed mapping itself.
	LastProcessedAtSymbol(symbol string) time.Time
}
