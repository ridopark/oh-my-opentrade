package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseThesisJSON_EntryThesis(t *testing.T) {
	et := domain.EntryThesis{
		BullArgument:   "strong momentum",
		BearArgument:   "overextended",
		JudgeReasoning: "momentum wins",
		Confidence:     0.8,
	}
	data, err := json.Marshal(et)
	require.NoError(t, err)

	gotET, gotAttr, err := domain.ParseThesisJSON(data)
	require.NoError(t, err)
	assert.NotNil(t, gotET)
	assert.Nil(t, gotAttr)
	assert.Equal(t, "strong momentum", gotET.BullArgument)
	assert.Equal(t, 0.8, gotET.Confidence)
}

func TestParseThesisJSON_TradeAttribution(t *testing.T) {
	attr := domain.TradeAttribution{
		V: 1,
		Confluence: domain.TradeAttributionConfluence{
			Score: 72,
			Components: []domain.TradeAttributionComponent{
				{Name: "ema_stack", Group: "technical", Weight: 15, Value: 3.0, Fired: true},
				{Name: "dp_buy", Group: "darkpool", Weight: 4, Value: 0.65, Fired: true},
			},
		},
		Regime:    "trend_up",
		VIXBucket: "low",
		Gates: []domain.TradeGate{
			{Name: "htf_bias", Passed: true},
		},
	}
	data, err := json.Marshal(attr)
	require.NoError(t, err)

	gotET, gotAttr, err := domain.ParseThesisJSON(data)
	require.NoError(t, err)
	assert.Nil(t, gotET)
	assert.NotNil(t, gotAttr)
	assert.Equal(t, 1, gotAttr.V)
	assert.Equal(t, 72, gotAttr.Confluence.Score)
	assert.Len(t, gotAttr.Confluence.Components, 2)
	assert.Equal(t, "ema_stack", gotAttr.Confluence.Components[0].Name)
	assert.Equal(t, "trend_up", gotAttr.Regime)
	assert.Len(t, gotAttr.Gates, 1)
	assert.True(t, gotAttr.Gates[0].Passed)
}

func TestParseThesisJSON_Empty(t *testing.T) {
	et, attr, err := domain.ParseThesisJSON(nil)
	assert.NoError(t, err)
	assert.Nil(t, et)
	assert.Nil(t, attr)

	et, attr, err = domain.ParseThesisJSON(json.RawMessage{})
	assert.NoError(t, err)
	assert.Nil(t, et)
	assert.Nil(t, attr)
}

func TestParseThesisJSON_Invalid(t *testing.T) {
	_, _, err := domain.ParseThesisJSON(json.RawMessage(`{invalid`))
	assert.Error(t, err)
}

func TestParseThesisJSON_NewFormatDoesNotBreakEntryThesisPath(t *testing.T) {
	attr := domain.TradeAttribution{V: 1, Confluence: domain.TradeAttributionConfluence{Score: 50}}
	data, _ := json.Marshal(attr)

	gotET, _, _ := domain.ParseThesisJSON(data)
	assert.Nil(t, gotET, "new-format thesis must NOT be parsed as EntryThesis")
}
