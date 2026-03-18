package strategy

import "fmt"

// AnchorSelection is the LLM's (or fallback ranker's) response selecting
// which candidate anchors to use for AVWAP computation.
type AnchorSelection struct {
	SelectedAnchors []SelectedAnchor
	Rationale       string
}

type SelectedAnchor struct {
	CandidateID string
	AnchorName  string
	Rank        int
	Confidence  float64
	Reason      string
}

func NewAnchorSelection(anchors []SelectedAnchor, rationale string) (AnchorSelection, error) {
	if len(anchors) == 0 {
		return AnchorSelection{}, fmt.Errorf("anchor selection: at least one selected anchor required")
	}

	ranks := make(map[int]bool, len(anchors))
	for i, a := range anchors {
		if a.CandidateID == "" {
			return AnchorSelection{}, fmt.Errorf("anchor selection: anchor[%d] has empty CandidateID", i)
		}
		if a.Rank < 1 {
			return AnchorSelection{}, fmt.Errorf("anchor selection: anchor[%d] rank must be >= 1, got %d", i, a.Rank)
		}
		if a.Confidence < 0 || a.Confidence > 1 {
			return AnchorSelection{}, fmt.Errorf("anchor selection: anchor[%d] confidence must be in [0,1], got %f", i, a.Confidence)
		}
		if ranks[a.Rank] {
			return AnchorSelection{}, fmt.Errorf("anchor selection: duplicate rank %d", a.Rank)
		}
		ranks[a.Rank] = true
	}

	return AnchorSelection{
		SelectedAnchors: anchors,
		Rationale:       rationale,
	}, nil
}
