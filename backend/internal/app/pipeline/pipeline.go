package pipeline

// Pipeline is the assembled graph of components that make up a single
// trading session — live, backtest, or replay. It does not own
// component construction (that lives in the bootstrap package); it
// owns the post-build wiring decisions: which subscribers to register,
// which Set* hooks to install, which mode-conditional behaviors apply.
//
// Subsequent PRs will populate Pipeline with the full set of services
// (event bus, ingestion, monitor, execution, position monitor, strategy
// runner) and migrate the per-root wiring from cmd/omo-core/services.go,
// internal/app/backtest/runner.go, and cmd/omo-replay/main.go into
// methods on Pipeline.
//
// This first cut keeps the struct intentionally bare so it can land
// without disturbing the existing composition roots. The Mode field is
// the single piece of behavioral signal subsequent migration steps will
// switch on.
type Pipeline struct {
	mode Mode
}

// New constructs an empty Pipeline for the given mode. Subsequent
// migration PRs will extend this constructor (and its parameters) to
// take the bootstrap-built components and run mode-specific wiring.
func New(mode Mode) *Pipeline {
	return &Pipeline{mode: mode}
}

// Mode returns the mode this pipeline was constructed for.
func (p *Pipeline) Mode() Mode { return p.mode }
