package strategy

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestExtractForcedContract_Valid(t *testing.T) {
	tags := map[string]string{
		TagForceExpiry:     "2026-04-27",
		TagForceStrike:     "205",
		TagForceRight:      "C",
		TagForceRefPremium: "2.11",
	}
	got, ok := extractForcedContract(tags)
	if !ok {
		t.Fatalf("extractForcedContract returned !ok for valid tags")
	}
	if got.Strike != 205 {
		t.Errorf("strike = %v, want 205", got.Strike)
	}
	if got.Right != domain.OptionRightCall {
		t.Errorf("right = %v, want CALL", got.Right)
	}
	if got.RefPremium != 2.11 {
		t.Errorf("ref_premium = %v, want 2.11", got.RefPremium)
	}
	wantExp := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	if !got.Expiry.Equal(wantExp) {
		t.Errorf("expiry = %v, want %v", got.Expiry, wantExp)
	}
}

func TestExtractForcedContract_PutVariants(t *testing.T) {
	for _, rightStr := range []string{"P", "PUT", "put", "p"} {
		tags := map[string]string{
			TagForceExpiry: "2026-04-27",
			TagForceStrike: "205",
			TagForceRight:  rightStr,
		}
		got, ok := extractForcedContract(tags)
		if !ok {
			t.Fatalf("%q not accepted as right", rightStr)
		}
		if got.Right != domain.OptionRightPut {
			t.Errorf("%q -> right = %v, want PUT", rightStr, got.Right)
		}
	}
}

func TestExtractForcedContract_MissingTagsReturnsFalse(t *testing.T) {
	full := map[string]string{
		TagForceExpiry: "2026-04-27",
		TagForceStrike: "205",
		TagForceRight:  "C",
	}
	for _, drop := range []string{TagForceExpiry, TagForceStrike, TagForceRight} {
		m := map[string]string{}
		for k, v := range full {
			if k == drop {
				continue
			}
			m[k] = v
		}
		if _, ok := extractForcedContract(m); ok {
			t.Errorf("expected !ok when %q missing", drop)
		}
	}
	if _, ok := extractForcedContract(nil); ok {
		t.Errorf("expected !ok for nil tags")
	}
	if _, ok := extractForcedContract(map[string]string{}); ok {
		t.Errorf("expected !ok for empty tags")
	}
}

func TestExtractForcedContract_Malformed(t *testing.T) {
	cases := []map[string]string{
		{TagForceExpiry: "not-a-date", TagForceStrike: "205", TagForceRight: "C"},
		{TagForceExpiry: "2026-04-27", TagForceStrike: "abc", TagForceRight: "C"},
		{TagForceExpiry: "2026-04-27", TagForceStrike: "-5", TagForceRight: "C"},
		{TagForceExpiry: "2026-04-27", TagForceStrike: "205", TagForceRight: "X"},
	}
	for i, tags := range cases {
		if _, ok := extractForcedContract(tags); ok {
			t.Errorf("case %d: expected !ok, got ok; tags=%v", i, tags)
		}
	}
}

func TestFindPinnedContract_Match(t *testing.T) {
	exp := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	chain := []domain.OptionContractSnapshot{
		mkSnap(exp, 200),
		mkSnap(exp, 205),
		mkSnap(exp, 210),
	}
	got, ok := findPinnedContract(chain, exp, 205)
	if !ok {
		t.Fatalf("expected match for strike=205")
	}
	if got.Strike != 205 {
		t.Errorf("strike = %v, want 205", got.Strike)
	}
}

func TestFindPinnedContract_StrikeNotFound(t *testing.T) {
	exp := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	chain := []domain.OptionContractSnapshot{mkSnap(exp, 200), mkSnap(exp, 210)}
	if _, ok := findPinnedContract(chain, exp, 205); ok {
		t.Errorf("expected no match for missing strike")
	}
}

func TestFindPinnedContract_ExpiryMismatch(t *testing.T) {
	wanted := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	other := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	chain := []domain.OptionContractSnapshot{mkSnap(other, 205)}
	if _, ok := findPinnedContract(chain, wanted, 205); ok {
		t.Errorf("expected no match for wrong-expiry chain")
	}
}

func TestFindPinnedContract_EmptyChain(t *testing.T) {
	exp := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	if _, ok := findPinnedContract(nil, exp, 205); ok {
		t.Errorf("expected no match for nil chain")
	}
}

// mkSnap builds a minimal OptionContractSnapshot for test assertions.
func mkSnap(expiry time.Time, strike float64) domain.OptionContractSnapshot {
	return domain.OptionContractSnapshot{
		OptionContract: domain.OptionContract{
			ContractSymbol: domain.Symbol("TEST"),
			Underlying:     domain.Symbol("TEST"),
			Expiry:         expiry,
			Strike:         strike,
			Right:          domain.OptionRightCall,
			Multiplier:     100,
		},
		OptionQuote: domain.OptionQuote{Bid: 1.0, Ask: 1.1, Last: 1.05},
	}
}
