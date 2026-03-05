package screener

import (
	"math"
	"testing"
	"time"
)

func TestNormalizeGap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"zero", 0, 0},
		{"ten", 10, 1.0},
		{"minus_ten", -10, -1.0},
		{"clamp_hi", 20, 1.0},
		{"minus_five", -5, -0.5},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeGap(tc.in)
			if got != tc.want {
				t.Fatalf("NormalizeGap(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeRVOL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"one", 1.0, 0},
		{"three", 3.0, 1.0},
		{"zero", 0, 0},
		{"nan", math.NaN(), 0},
		{"inf", math.Inf(1), 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeRVOL(tc.in)
			if got != tc.want {
				t.Fatalf("NormalizeRVOL(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestScreenerResultValidate(t *testing.T) {
	t.Parallel()
	base := ScreenerResult{
		TenantID: "t1",
		EnvMode:  "paper",
		RunID:    "run1",
		AsOf:     time.Now().UTC(),
		Symbol:   "AAPL",
		Status:   DataStatusOK,
	}

	t.Run("valid_passes", func(t *testing.T) {
		t.Parallel()
		if err := base.Validate(); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("empty_tenant_fails", func(t *testing.T) {
		t.Parallel()
		r := base
		r.TenantID = ""
		if err := r.Validate(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("empty_env_fails", func(t *testing.T) {
		t.Parallel()
		r := base
		r.EnvMode = ""
		if err := r.Validate(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("empty_runid_fails", func(t *testing.T) {
		t.Parallel()
		r := base
		r.RunID = ""
		if err := r.Validate(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("zero_asof_fails", func(t *testing.T) {
		t.Parallel()
		r := base
		r.AsOf = time.Time{}
		if err := r.Validate(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("empty_symbol_fails", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Symbol = ""
		if err := r.Validate(); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("empty_status_fails", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Status = ""
		if err := r.Validate(); err == nil {
			t.Fatalf("expected error")
		}
	})
}
