package strategy

import "testing"

func TestInferParamSchema_TypeInference(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    any
		wantType string
	}{
		{"int64", "hold_bars", int64(8), "integer"},
		{"float64", "atr_multiplier", float64(3.0), "number"},
		{"bool", "enabled", true, "boolean"},
		{"string", "name", "test", "string"},
		{"string_slice", "allowed_regimes", []string{"up", "down"}, "string_array"},
		{"any_slice", "tags", []any{"a", "b"}, "string_array"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]any{tt.key: tt.value}
			schema := InferParamSchema(params, nil)
			if len(schema) != 1 {
				t.Fatalf("expected 1 param, got %d", len(schema))
			}
			if schema[0].Type != tt.wantType {
				t.Errorf("type = %q, want %q", schema[0].Type, tt.wantType)
			}
			if tt.wantType != "string_array" {
				if schema[0].Default != tt.value {
					t.Errorf("default = %v, want %v", schema[0].Default, tt.value)
				}
			}
		})
	}
}

func TestInferParamSchema_GroupInference(t *testing.T) {
	params := map[string]any{
		"hold_bars":                     int64(8),
		"regime_filter.allowed_regimes": []string{"up"},
		"dynamic_risk.enabled":          true,
	}

	schema := InferParamSchema(params, nil)
	groups := make(map[string]string, len(schema))
	for _, m := range schema {
		groups[m.Key] = m.Group
	}

	if groups["hold_bars"] != "Strategy Params" {
		t.Errorf("hold_bars group = %q, want %q", groups["hold_bars"], "Strategy Params")
	}
	if groups["regime_filter.allowed_regimes"] != "Regime Filter" {
		t.Errorf("regime_filter group = %q, want %q", groups["regime_filter.allowed_regimes"], "Regime Filter")
	}
	if groups["dynamic_risk.enabled"] != "Dynamic Risk" {
		t.Errorf("dynamic_risk group = %q, want %q", groups["dynamic_risk.enabled"], "Dynamic Risk")
	}
}

func TestInferParamSchema_KnownRanges(t *testing.T) {
	params := map[string]any{
		"hold_bars":      int64(8),
		"atr_multiplier": float64(3.0),
	}

	schema := InferParamSchema(params, nil)
	byKey := make(map[string]ParamMeta, len(schema))
	for _, m := range schema {
		byKey[m.Key] = m
	}

	hb := byKey["hold_bars"]
	if hb.Min == nil || *hb.Min != 1 {
		t.Errorf("hold_bars min = %v, want 1", hb.Min)
	}
	if hb.Max == nil || *hb.Max != 50 {
		t.Errorf("hold_bars max = %v, want 50", hb.Max)
	}
	if hb.Step == nil || *hb.Step != 1 {
		t.Errorf("hold_bars step = %v, want 1", hb.Step)
	}

	atr := byKey["atr_multiplier"]
	if atr.Min == nil || *atr.Min != 0.5 {
		t.Errorf("atr_multiplier min = %v, want 0.5", atr.Min)
	}
	if atr.Max == nil || *atr.Max != 10 {
		t.Errorf("atr_multiplier max = %v, want 10", atr.Max)
	}
}

func TestInferParamSchema_Descriptions(t *testing.T) {
	params := map[string]any{
		"hold_bars": int64(8),
		"custom":    "val",
	}
	overrides := map[string]string{
		"custom": "A custom parameter",
	}

	schema := InferParamSchema(params, overrides)
	byKey := make(map[string]ParamMeta, len(schema))
	for _, m := range schema {
		byKey[m.Key] = m
	}

	if byKey["hold_bars"].Description == "" {
		t.Error("hold_bars should have a known description")
	}
	if byKey["custom"].Description != "A custom parameter" {
		t.Errorf("custom description = %q, want %q", byKey["custom"].Description, "A custom parameter")
	}
}

func TestInferParamSchema_SortOrder(t *testing.T) {
	params := map[string]any{
		"z_param":                int64(1),
		"a_param":                int64(2),
		"regime_filter.strength": float64(0.5),
	}

	schema := InferParamSchema(params, nil)
	if len(schema) != 3 {
		t.Fatalf("expected 3 params, got %d", len(schema))
	}

	if schema[0].Group != "Regime Filter" {
		t.Errorf("first group = %q, want Regime Filter (sorts before Strategy Params)", schema[0].Group)
	}
	if schema[1].Key != "a_param" {
		t.Errorf("second key = %q, want a_param (alphabetical within group)", schema[1].Key)
	}
	if schema[2].Key != "z_param" {
		t.Errorf("third key = %q, want z_param", schema[2].Key)
	}
}

func TestInferParamSchema_Empty(t *testing.T) {
	schema := InferParamSchema(nil, nil)
	if schema != nil {
		t.Errorf("expected nil for empty params, got %v", schema)
	}

	schema = InferParamSchema(map[string]any{}, nil)
	if schema != nil {
		t.Errorf("expected nil for empty map, got %v", schema)
	}
}
