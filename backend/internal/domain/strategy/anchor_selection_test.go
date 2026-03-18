package strategy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAnchorSelection_Valid(t *testing.T) {
	anchors := []SelectedAnchor{
		{CandidateID: "swing_high_5m_1000", AnchorName: "swing_high_5m_1000", Rank: 1, Confidence: 0.9, Reason: "strong level"},
		{CandidateID: "swing_low_1h_2000", AnchorName: "swing_low_1h_2000", Rank: 2, Confidence: 0.7, Reason: "tested 3x"},
	}
	sel, err := NewAnchorSelection(anchors, "two key structural levels")
	require.NoError(t, err)
	assert.Len(t, sel.SelectedAnchors, 2)
	assert.Equal(t, "two key structural levels", sel.Rationale)
}

func TestNewAnchorSelection_Empty(t *testing.T) {
	_, err := NewAnchorSelection(nil, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one")

	_, err = NewAnchorSelection([]SelectedAnchor{}, "")
	assert.Error(t, err)
}

func TestNewAnchorSelection_EmptyCandidateID(t *testing.T) {
	anchors := []SelectedAnchor{
		{CandidateID: "", Rank: 1, Confidence: 0.5},
	}
	_, err := NewAnchorSelection(anchors, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty CandidateID")
}

func TestNewAnchorSelection_DuplicateRanks(t *testing.T) {
	anchors := []SelectedAnchor{
		{CandidateID: "a", Rank: 1, Confidence: 0.9},
		{CandidateID: "b", Rank: 1, Confidence: 0.8},
	}
	_, err := NewAnchorSelection(anchors, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate rank")
}

func TestNewAnchorSelection_InvalidRank(t *testing.T) {
	anchors := []SelectedAnchor{
		{CandidateID: "a", Rank: 0, Confidence: 0.5},
	}
	_, err := NewAnchorSelection(anchors, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rank must be >= 1")
}

func TestNewAnchorSelection_ConfidenceOutOfRange(t *testing.T) {
	tests := []struct {
		name       string
		confidence float64
	}{
		{"negative", -0.1},
		{"above one", 1.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anchors := []SelectedAnchor{
				{CandidateID: "a", Rank: 1, Confidence: tt.confidence},
			}
			_, err := NewAnchorSelection(anchors, "")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "confidence must be in [0,1]")
		})
	}
}

func TestNewAnchorSelection_BoundaryConfidence(t *testing.T) {
	anchors := []SelectedAnchor{
		{CandidateID: "a", Rank: 1, Confidence: 0.0},
		{CandidateID: "b", Rank: 2, Confidence: 1.0},
	}
	sel, err := NewAnchorSelection(anchors, "")
	require.NoError(t, err)
	assert.Equal(t, 0.0, sel.SelectedAnchors[0].Confidence)
	assert.Equal(t, 1.0, sel.SelectedAnchors[1].Confidence)
}
