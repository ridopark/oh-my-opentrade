package pipeline

// Pipeline is the assembled graph of components that make up a single
// trading session — live, backtest, or replay. It owns the post-build
// wiring decisions: which subscribers to register, which Set* hooks to
// install, which mode-conditional behaviors apply.
type Pipeline struct {
	mode Mode
}

func New(mode Mode) *Pipeline {
	return &Pipeline{mode: mode}
}

func (p *Pipeline) Mode() Mode { return p.mode }
