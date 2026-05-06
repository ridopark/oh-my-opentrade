package backtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// stubLiveMarket satisfies ports.OptionsMarketDataPort for the inverse-case
// validation test. It is never invoked because validation runs before any
// chain lookup.
type stubLiveMarket struct{}

func (stubLiveMarket) GetOptionChain(
	_ context.Context,
	_ domain.Symbol,
	_ time.Time,
	_ domain.OptionRight,
	_, _ int,
) ([]domain.OptionContractSnapshot, error) {
	return nil, nil
}

var _ ports.OptionsMarketDataPort = stubLiveMarket{}

func TestValidateRunConfig(t *testing.T) {
	cases := []struct {
		name      string
		cfg       RunConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name: "prefer_live_chain true with nil live market errors",
			cfg: RunConfig{
				PreferLiveChain:   true,
				LiveOptionsMarket: nil,
			},
			wantErr:   true,
			errSubstr: "prefer_live_chain",
		},
		{
			name: "prefer_live_chain true with nil live market mentions not provided",
			cfg: RunConfig{
				PreferLiveChain:   true,
				LiveOptionsMarket: nil,
			},
			wantErr:   true,
			errSubstr: "not provided",
		},
		{
			name: "prefer_live_chain false with non-nil live market is no-op",
			cfg: RunConfig{
				PreferLiveChain:   false,
				LiveOptionsMarket: stubLiveMarket{},
			},
			wantErr: false,
		},
		{
			name: "prefer_live_chain false with nil live market is no-op",
			cfg: RunConfig{
				PreferLiveChain:   false,
				LiveOptionsMarket: nil,
			},
			wantErr: false,
		},
		{
			name: "prefer_live_chain true with non-nil live market passes",
			cfg: RunConfig{
				PreferLiveChain:   true,
				LiveOptionsMarket: stubLiveMarket{},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRunConfig(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateRunConfig: want error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("validateRunConfig: error %q does not contain %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateRunConfig: want nil error, got %v", err)
			}
		})
	}
}

func TestSameCalendarDayET(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}

	cases := []struct {
		name string
		a, b time.Time
		want bool
	}{
		{
			name: "same ET day",
			a:    time.Date(2026, 5, 5, 9, 30, 0, 0, loc),
			b:    time.Date(2026, 5, 5, 15, 59, 0, 0, loc),
			want: true,
		},
		{
			name: "different ET day",
			a:    time.Date(2026, 5, 4, 9, 30, 0, 0, loc),
			b:    time.Date(2026, 5, 5, 9, 30, 0, 0, loc),
			want: false,
		},
		{
			name: "UTC midnight rollover lands same ET day",
			// 2026-05-06 03:00 UTC == 2026-05-05 23:00 ET
			a:    time.Date(2026, 5, 5, 23, 0, 0, 0, loc),
			b:    time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "ET midnight rollover splits days",
			// 2026-05-06 04:00 UTC == 2026-05-06 00:00 ET, 2026-05-05 23:59 ET differs
			a:    time.Date(2026, 5, 5, 23, 59, 0, 0, loc),
			b:    time.Date(2026, 5, 6, 4, 0, 0, 0, time.UTC),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameCalendarDayET(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("sameCalendarDayET(%v, %v) = %v; want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
