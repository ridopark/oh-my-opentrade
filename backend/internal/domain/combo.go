package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ComboType identifies the structure of a multi-leg options order. The first
// ship supports 2-leg vertical debit/credit spreads only; 4-leg structures
// (iron condors, butterflies) and time spreads (calendars) are tracked as
// TODO(sprint5) follow-ups.
type ComboType string

const (
	ComboTypeVerticalCallDebit  ComboType = "vertical_call_debit"
	ComboTypeVerticalPutDebit   ComboType = "vertical_put_debit"
	ComboTypeVerticalCallCredit ComboType = "vertical_call_credit"
	ComboTypeVerticalPutCredit  ComboType = "vertical_put_credit"
)

// IsDebit returns true when the combo is paid for on entry (long option premium
// exceeds short option premium). Debit combos have capped risk equal to the
// net debit paid. Credit combos collect premium and are capped at strike width
// minus credit received.
func (c ComboType) IsDebit() bool {
	switch c {
	case ComboTypeVerticalCallDebit, ComboTypeVerticalPutDebit:
		return true
	}
	return false
}

// ComboLeg represents a single option leg of a multi-leg combo order. Ratio
// encodes both size and side: positive = buy (long), negative = sell (short).
// For a 1:1 vertical spread, one leg has ratio +1 and the other -1.
type ComboLeg struct {
	Symbol    Symbol         `json:"symbol"`    // underlying ticker (e.g. "AAPL")
	Right     OptionRight    `json:"right"`     // CALL or PUT
	Strike    float64        `json:"strike"`    // dollar strike
	Expiry    time.Time      `json:"expiry"`    // contract expiration (market close ET)
	Ratio     int            `json:"ratio"`     // +N buy, -N sell; zero is invalid
	AssetType InstrumentType `json:"assetType"` // always OPTION in Sprint 5 first-ship
}

// OCCSymbol renders the OCC contract symbol for this leg, useful for logging,
// journal rows, and downstream option-quote lookups.
func (l ComboLeg) OCCSymbol() Symbol {
	return Symbol(FormatOCCSymbol(string(l.Symbol), l.Expiry, l.Right, l.Strike))
}

// IsCombo reports whether this intent is a multi-leg BAG order. The combo path
// only activates when true; equity and single-leg option routing is untouched.
func (o OrderIntent) IsCombo() bool {
	return len(o.Legs) > 0
}

// NewComboOrderIntent constructs a validated combo OrderIntent for a 2-leg
// vertical debit/credit spread. Validations:
//   - exactly 2 legs (Sprint 5 first-ship scope)
//   - same underlying across legs
//   - same expiry across legs
//   - ratios non-zero and sum to zero (one +N, one -N for verticals)
//   - all legs are options
//
// The underlying symbol, strategy, rationale, and idempotency key carry the
// usual OrderIntent semantics. LimitPrice here is the net debit (positive) or
// net credit (positive amount collected); MaxLossUSD is required and enforces
// hard capped risk at the gate layer.
func NewComboOrderIntent(
	id uuid.UUID,
	tenantID string,
	envMode EnvMode,
	underlying Symbol,
	comboType ComboType,
	legs []ComboLeg,
	netLimitPrice float64,
	quantity float64,
	strategy, rationale string,
	confidence float64,
	idempotencyKey string,
	maxLossUSD float64,
) (OrderIntent, error) {
	if idempotencyKey == "" {
		return OrderIntent{}, errors.New("idempotency key is required")
	}
	if len(legs) != 2 {
		return OrderIntent{}, fmt.Errorf("combo first-ship supports exactly 2 legs, got %d", len(legs))
	}
	if quantity <= 0 {
		return OrderIntent{}, errors.New("quantity must be > 0")
	}
	if netLimitPrice <= 0 {
		return OrderIntent{}, errors.New("net limit price must be > 0")
	}
	if confidence < 0 || confidence > 1 {
		return OrderIntent{}, fmt.Errorf("confidence must be between 0 and 1, got %v", confidence)
	}
	if maxLossUSD <= 0 {
		return OrderIntent{}, errors.New("MaxLossUSD must be > 0 for combo orders")
	}

	underlyingStr := string(underlying)
	ratioSum := 0
	for i, leg := range legs {
		if leg.AssetType != InstrumentTypeOption {
			return OrderIntent{}, fmt.Errorf("leg %d: asset type must be OPTION", i)
		}
		if leg.Ratio == 0 {
			return OrderIntent{}, fmt.Errorf("leg %d: ratio must be non-zero", i)
		}
		if string(leg.Symbol) != underlyingStr {
			return OrderIntent{}, fmt.Errorf("leg %d: underlying %q does not match combo underlying %q", i, leg.Symbol, underlying)
		}
		if leg.Strike <= 0 {
			return OrderIntent{}, fmt.Errorf("leg %d: strike must be > 0", i)
		}
		if !leg.Expiry.Equal(legs[0].Expiry) {
			return OrderIntent{}, fmt.Errorf("leg %d: expiry %s does not match leg 0 expiry %s", i, leg.Expiry, legs[0].Expiry)
		}
		ratioSum += leg.Ratio
	}
	if ratioSum != 0 {
		return OrderIntent{}, fmt.Errorf("vertical spread ratios must sum to zero (one long, one short); got %d", ratioSum)
	}

	// Cache leg slice so callers can mutate theirs without affecting the intent.
	legsCopy := make([]ComboLeg, len(legs))
	copy(legsCopy, legs)

	// Derive an Instrument marker so existing code paths that key off
	// InstrumentType continue to recognize this as an options flow.
	inst := Instrument{
		Type:             InstrumentTypeOption,
		Symbol:           underlying,
		UnderlyingSymbol: underlying,
	}

	return OrderIntent{
		ID:             id,
		TenantID:       tenantID,
		EnvMode:        envMode,
		Symbol:         underlying,
		Direction:      DirectionLong, // combo direction is encoded via ComboType; use LONG as a placeholder
		LimitPrice:     netLimitPrice,
		StopLoss:       netLimitPrice, // capped-risk structures enforce risk via MaxLossUSD
		MaxSlippageBPS: 0,
		Quantity:       quantity,
		Strategy:       strategy,
		Rationale:      rationale,
		Confidence:     confidence,
		IdempotencyKey: idempotencyKey,
		Instrument:     &inst,
		AssetClass:     AssetClassEquity, // underlying asset class
		MaxLossUSD:     maxLossUSD,
		Legs:           legsCopy,
		ComboType:      comboType,
	}, nil
}

// ComboRisk returns the maximum dollar loss for a combo OrderIntent. The
// portfolio-heat and directional-bias gates use this instead of the single-leg
// |entry - stop| * qty formula.
//
// Debit spreads: risk = net debit paid * quantity * multiplier (legs
// contribute the multiplier; equity options = 100).
// Credit spreads: risk = (strike width - credit received) * quantity * multiplier.
// A result of 0 means "unable to infer" (should not happen for validated
// combos; callers fall back to MaxLossUSD in that case).
func ComboRisk(intent OrderIntent) float64 {
	if !intent.IsCombo() {
		return 0
	}
	const optionMultiplier = 100.0
	if intent.ComboType.IsDebit() {
		return intent.LimitPrice * intent.Quantity * optionMultiplier
	}
	// credit spread: width - credit
	width := comboStrikeWidth(intent.Legs)
	if width <= 0 {
		// Can't infer — let caller fall back to MaxLossUSD.
		return 0
	}
	// LimitPrice for a credit intent is the premium *collected* (positive).
	netCredit := intent.LimitPrice
	risk := (width - netCredit) * intent.Quantity * optionMultiplier
	if risk < 0 {
		return 0
	}
	return risk
}

// comboStrikeWidth returns the absolute dollar distance between the two leg
// strikes in a 2-leg vertical. Returns 0 if legs aren't a valid 2-leg spread.
func comboStrikeWidth(legs []ComboLeg) float64 {
	if len(legs) != 2 {
		return 0
	}
	w := legs[0].Strike - legs[1].Strike
	if w < 0 {
		w = -w
	}
	return w
}
