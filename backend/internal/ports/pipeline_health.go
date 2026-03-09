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
}
