package options

import (
	"math"
	"testing"
)

func TestAdjustIV_VIXBeta(t *testing.T) {
	t.Run("VIX spike scales IV up", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry: 15.0,
			VIXNow:     18.0, // +20% VIX spike
			VIXBeta:    1.0,
		}
		result := AdjustIV(0.30, adj)
		// 0.30 * (18/15)^1.0 = 0.30 * 1.20 = 0.36
		if math.Abs(result-0.36) > 0.001 {
			t.Errorf("expected ~0.36, got %f", result)
		}
	})

	t.Run("VIX drop scales IV down", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry: 20.0,
			VIXNow:     16.0, // -20% VIX drop
			VIXBeta:    1.0,
		}
		result := AdjustIV(0.30, adj)
		// 0.30 * (16/20)^1.0 = 0.30 * 0.80 = 0.24
		if math.Abs(result-0.24) > 0.001 {
			t.Errorf("expected ~0.24, got %f", result)
		}
	})

	t.Run("beta < 1 dampens VIX effect", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry: 15.0,
			VIXNow:     18.0,
			VIXBeta:    0.5,
		}
		result := AdjustIV(0.30, adj)
		// 0.30 * (1.20)^0.5 = 0.30 * 1.0954 ≈ 0.3286
		expected := 0.30 * math.Pow(1.2, 0.5)
		if math.Abs(result-expected) > 0.001 {
			t.Errorf("expected ~%.4f, got %f", expected, result)
		}
	})

	t.Run("zero beta disables VIX scaling", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry: 15.0,
			VIXNow:     25.0,
			VIXBeta:    0.0, // disabled
		}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("zero beta should return unchanged IV, got %f", result)
		}
	})

	t.Run("missing VIX prices disable scaling", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry: 0, // missing
			VIXNow:     18.0,
			VIXBeta:    1.0,
		}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("missing VIX entry should return unchanged IV, got %f", result)
		}
	})
}

func TestAdjustIV_TODSeasonality(t *testing.T) {
	t.Run("opening premium (+4%)", func(t *testing.T) {
		adj := IVAdjustment{
			TODSeasonalEnabled: true,
			MinutesSinceOpen:   10, // 9:40 ET
		}
		result := AdjustIV(0.30, adj)
		expected := 0.30 * 1.04
		if math.Abs(result-expected) > 0.001 {
			t.Errorf("expected ~%.4f, got %f", expected, result)
		}
	})

	t.Run("midday dip (-2.5%)", func(t *testing.T) {
		adj := IVAdjustment{
			TODSeasonalEnabled: true,
			MinutesSinceOpen:   200, // ~12:50 ET
		}
		result := AdjustIV(0.30, adj)
		expected := 0.30 * 0.975
		if math.Abs(result-expected) > 0.001 {
			t.Errorf("expected ~%.4f, got %f", expected, result)
		}
	})

	t.Run("closing approach (+1.5%)", func(t *testing.T) {
		adj := IVAdjustment{
			TODSeasonalEnabled: true,
			MinutesSinceOpen:   370, // 15:40 ET
		}
		result := AdjustIV(0.30, adj)
		expected := 0.30 * 1.015
		if math.Abs(result-expected) > 0.001 {
			t.Errorf("expected ~%.4f, got %f", expected, result)
		}
	})

	t.Run("disabled returns unchanged IV", func(t *testing.T) {
		adj := IVAdjustment{
			TODSeasonalEnabled: false,
			MinutesSinceOpen:   10,
		}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("disabled TOD should return unchanged IV, got %f", result)
		}
	})

	t.Run("negative minutes clamped to 0", func(t *testing.T) {
		adj := IVAdjustment{
			TODSeasonalEnabled: true,
			MinutesSinceOpen:   -10,
		}
		result := AdjustIV(0.30, adj)
		expected := 0.30 * 1.04 // clamped to 0, which is first bucket
		if math.Abs(result-expected) > 0.001 {
			t.Errorf("expected ~%.4f, got %f", expected, result)
		}
	})
}

func TestAdjustIV_EarningsRamp(t *testing.T) {
	t.Run("1 day to earnings — max ramp", func(t *testing.T) {
		adj := IVAdjustment{
			EarningsRampEnabled: true,
			DaysToEarnings:      1,
		}
		result := AdjustIV(0.30, adj)
		// sqrt(5)/sqrt(1) = 2.236
		expected := 0.30 * math.Sqrt(5.0)
		if math.Abs(result-expected) > 0.001 {
			t.Errorf("expected ~%.4f, got %f", expected, result)
		}
	})

	t.Run("5 days to earnings — baseline (1.0x)", func(t *testing.T) {
		adj := IVAdjustment{
			EarningsRampEnabled: true,
			DaysToEarnings:      5,
		}
		result := AdjustIV(0.30, adj)
		if math.Abs(result-0.30) > 0.001 {
			t.Errorf("5-day baseline should be ~0.30, got %f", result)
		}
	})

	t.Run("8 days to earnings — capped at 1.0", func(t *testing.T) {
		adj := IVAdjustment{
			EarningsRampEnabled: true,
			DaysToEarnings:      8,
		}
		result := AdjustIV(0.30, adj)
		// sqrt(5)/sqrt(8) = 0.79 -> capped at 1.0
		if math.Abs(result-0.30) > 0.001 {
			t.Errorf("8-day should be capped at 1.0x, got %f", result)
		}
	})

	t.Run("11 days to earnings — no adjustment", func(t *testing.T) {
		adj := IVAdjustment{
			EarningsRampEnabled: true,
			DaysToEarnings:      11,
		}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf(">10 days should return unchanged IV, got %f", result)
		}
	})

	t.Run("0 days — no adjustment", func(t *testing.T) {
		adj := IVAdjustment{
			EarningsRampEnabled: true,
			DaysToEarnings:      0,
		}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("0 days should return unchanged IV, got %f", result)
		}
	})

	t.Run("disabled returns unchanged IV", func(t *testing.T) {
		adj := IVAdjustment{
			EarningsRampEnabled: false,
			DaysToEarnings:      1,
		}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("disabled earnings ramp should return unchanged IV, got %f", result)
		}
	})
}

func TestAdjustIV_MoveCrush(t *testing.T) {
	t.Run("call crushes harder than put on favorable 2% move", func(t *testing.T) {
		base := 0.30
		// Call on +2% move (favorable) vs put on -2% move (favorable).
		callAdj := IVAdjustment{MoveCrushEnabled: true, MoveCrushCallK: 0.6, MoveCrushPutK: 0.4, MoveCrushFloor: 0.5, UnderlyingRetPct: 0.02, IsCall: true}
		putAdj := IVAdjustment{MoveCrushEnabled: true, MoveCrushCallK: 0.6, MoveCrushPutK: 0.4, MoveCrushFloor: 0.5, UnderlyingRetPct: -0.02, IsCall: false}
		callIV := AdjustIV(base, callAdj)
		putIV := AdjustIV(base, putAdj)
		// Call: 0.30 * (1 - 0.6*0.02) = 0.30 * 0.988 = 0.2964
		// Put:  0.30 * (1 - 0.4*0.02) = 0.30 * 0.992 = 0.2976
		if math.Abs(callIV-0.2964) > 0.0001 {
			t.Errorf("call crushed IV expected 0.2964, got %f", callIV)
		}
		if math.Abs(putIV-0.2976) > 0.0001 {
			t.Errorf("put crushed IV expected 0.2976, got %f", putIV)
		}
		if callIV >= putIV {
			t.Errorf("call IV (%f) should crush harder than put (%f) at the same magnitude", callIV, putIV)
		}
	})

	t.Run("adverse move leaves call IV unchanged", func(t *testing.T) {
		// Call with underlying DOWN 2% — losing trade. IV does not crush
		// (skew typically supports the option on adverse moves); leave
		// untouched so the losing trade prices out via its real terminal IV.
		adj := IVAdjustment{MoveCrushEnabled: true, MoveCrushCallK: 0.6, MoveCrushFloor: 0.5, UnderlyingRetPct: -0.02, IsCall: true}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("adverse-move call should leave IV unchanged, got %f", result)
		}
	})

	t.Run("put crushes on down-move", func(t *testing.T) {
		adj := IVAdjustment{MoveCrushEnabled: true, MoveCrushPutK: 0.4, MoveCrushFloor: 0.5, UnderlyingRetPct: -0.02, IsCall: false}
		result := AdjustIV(0.30, adj)
		expected := 0.30 * (1 - 0.4*0.02)
		if math.Abs(result-expected) > 0.0001 {
			t.Errorf("favorable-move put should crush; expected ~%.4f, got %f", expected, result)
		}
	})

	t.Run("put on up-move leaves IV unchanged (adverse)", func(t *testing.T) {
		adj := IVAdjustment{MoveCrushEnabled: true, MoveCrushPutK: 0.4, UnderlyingRetPct: 0.02, IsCall: false}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("adverse-move put should leave IV unchanged, got %f", result)
		}
	})

	t.Run("floor prevents runaway crush on huge move", func(t *testing.T) {
		adj := IVAdjustment{MoveCrushEnabled: true, MoveCrushCallK: 2.0, MoveCrushFloor: 0.5, UnderlyingRetPct: 0.50, IsCall: true}
		result := AdjustIV(0.30, adj)
		// Raw multiplier would be 1 - 2.0*0.50 = 0, floored at 0.5
		expected := 0.30 * 0.5
		if math.Abs(result-expected) > 0.0001 {
			t.Errorf("expected floor-clamped %.4f, got %f", expected, result)
		}
	})

	t.Run("zero k disables path for that side", func(t *testing.T) {
		adj := IVAdjustment{MoveCrushEnabled: true, MoveCrushCallK: 0.0, MoveCrushPutK: 0.4, UnderlyingRetPct: 0.05, IsCall: true}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("zero k for calls should leave IV unchanged, got %f", result)
		}
	})

	t.Run("disabled flag leaves IV unchanged", func(t *testing.T) {
		adj := IVAdjustment{MoveCrushEnabled: false, MoveCrushCallK: 0.6, UnderlyingRetPct: 0.02, IsCall: true}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("disabled move crush should leave IV unchanged, got %f", result)
		}
	})

	t.Run("zero return leaves IV unchanged", func(t *testing.T) {
		adj := IVAdjustment{MoveCrushEnabled: true, MoveCrushCallK: 0.6, UnderlyingRetPct: 0, IsCall: true}
		result := AdjustIV(0.30, adj)
		if result != 0.30 {
			t.Errorf("zero underlying return should leave IV unchanged, got %f", result)
		}
	})
}

func TestAdjustIV_CombinedEffects(t *testing.T) {
	t.Run("all three adjustments stack multiplicatively", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry:          15.0,
			VIXNow:              18.0,
			VIXBeta:             1.0,
			TODSeasonalEnabled:  true,
			MinutesSinceOpen:    10, // opening: 1.04
			EarningsRampEnabled: true,
			DaysToEarnings:      2, // sqrt(5)/sqrt(2) ≈ 1.581
		}
		result := AdjustIV(0.30, adj)
		vixFactor := 18.0 / 15.0              // 1.20
		todFactor := 1.04                      // opening
		earnFactor := math.Sqrt(5.0 / 2.0)    // 1.581
		expected := 0.30 * vixFactor * todFactor * earnFactor
		if math.Abs(result-expected) > 0.001 {
			t.Errorf("expected ~%.4f, got %f", expected, result)
		}
	})

	t.Run("result clamped to minimum 0.01", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry: 100.0,
			VIXNow:     1.0,
			VIXBeta:    2.0,
		}
		result := AdjustIV(0.30, adj)
		if result < 0.01 {
			t.Errorf("result should be clamped to 0.01 minimum, got %f", result)
		}
	})

	t.Run("result clamped to maximum 5.0", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry: 1.0,
			VIXNow:     100.0,
			VIXBeta:    2.0,
		}
		result := AdjustIV(4.0, adj)
		if result > 5.0 {
			t.Errorf("result should be clamped to 5.0 maximum, got %f", result)
		}
	})

	t.Run("zero base IV returned unchanged", func(t *testing.T) {
		adj := IVAdjustment{
			VIXAtEntry:         15.0,
			VIXNow:             18.0,
			VIXBeta:            1.0,
			TODSeasonalEnabled: true,
			MinutesSinceOpen:   10,
		}
		result := AdjustIV(0, adj)
		if result != 0 {
			t.Errorf("zero base IV should return 0, got %f", result)
		}
	})
}

func TestTODSeasonalMultiplier(t *testing.T) {
	tests := []struct {
		name     string
		minutes  int
		expected float64
	}{
		{"market open", 0, 1.04},
		{"9:45", 15, 1.04},
		{"10:00", 30, 1.04},
		{"10:01", 31, 0.985},
		{"11:00", 90, 0.985},
		{"11:31", 121, 0.975},
		{"13:00", 210, 0.975},
		{"14:01", 271, 0.99},
		{"15:00", 330, 0.99},
		{"15:01", 331, 1.015},
		{"15:59", 389, 1.015},
		{"16:00", 390, 1.015},
		{"past close (clamped)", 500, 1.015},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := todSeasonalMultiplier(tc.minutes)
			if math.Abs(result-tc.expected) > 0.0001 {
				t.Errorf("at %d min, expected %.4f, got %.4f", tc.minutes, tc.expected, result)
			}
		})
	}
}

func TestEarningsRampMultiplier(t *testing.T) {
	tests := []struct {
		days     int
		expected float64
	}{
		{0, 1.0},
		{-1, 1.0},
		{1, math.Sqrt(5.0)},
		{2, math.Sqrt(5.0 / 2.0)},
		{3, math.Sqrt(5.0 / 3.0)},
		{5, 1.0},
		{7, 1.0},  // sqrt(5/7) < 1, capped
		{10, 1.0}, // sqrt(5/10) < 1, capped
		{11, 1.0}, // beyond range
		{20, 1.0},
	}
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			result := earningsRampMultiplier(tc.days)
			if math.Abs(result-tc.expected) > 0.0001 {
				t.Errorf("at %d days, expected %.4f, got %.4f", tc.days, tc.expected, result)
			}
		})
	}
}
