package strategy

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// forcedContract is a pinned option contract carried in Signal.Tags.
// It bypasses ContractSelectionService's delta/DTE/OI/spread screening —
// used by strategies (copytrade) that already know the exact contract.
type forcedContract struct {
	Expiry     time.Time
	Strike     float64
	Right      domain.OptionRight
	RefPremium float64 // optional: author-stated fill price, for logging only
}

// Tag keys. Kept as constants so strategy code and risk_sizer agree on names.
const (
	TagForceExpiry     = "force_expiry"      // "YYYY-MM-DD"
	TagForceStrike     = "force_strike"      // float as string
	TagForceRight      = "force_right"       // "C" | "P" | "CALL" | "PUT"
	TagForceRefPremium = "force_ref_premium" // float as string; optional
)

// extractForcedContract returns a pinned contract and true if all three
// required tags are present and parseable. Partial sets return false.
func extractForcedContract(tags map[string]string) (forcedContract, bool) {
	if tags == nil {
		return forcedContract{}, false
	}
	expStr := strings.TrimSpace(tags[TagForceExpiry])
	strikeStr := strings.TrimSpace(tags[TagForceStrike])
	rightStr := strings.TrimSpace(tags[TagForceRight])
	if expStr == "" || strikeStr == "" || rightStr == "" {
		return forcedContract{}, false
	}
	expiry, err := time.Parse("2006-01-02", expStr)
	if err != nil {
		return forcedContract{}, false
	}
	strike, err := strconv.ParseFloat(strikeStr, 64)
	if err != nil || strike <= 0 {
		return forcedContract{}, false
	}
	var right domain.OptionRight
	switch strings.ToUpper(rightStr) {
	case "C", "CALL":
		right = domain.OptionRightCall
	case "P", "PUT":
		right = domain.OptionRightPut
	default:
		return forcedContract{}, false
	}
	var refPremium float64
	if rp := strings.TrimSpace(tags[TagForceRefPremium]); rp != "" {
		if v, err := strconv.ParseFloat(rp, 64); err == nil {
			refPremium = v
		}
	}
	return forcedContract{
		Expiry:     expiry,
		Strike:     strike,
		Right:      right,
		RefPremium: refPremium,
	}, true
}

// findPinnedContract locates the snapshot in `chain` matching both the
// pinned expiry (calendar date) and strike (to within a cent). Right is
// already filtered by the chain fetch call.
func findPinnedContract(chain []domain.OptionContractSnapshot, expiry time.Time, strike float64) (domain.OptionContractSnapshot, bool) {
	expDate := expiry.Format("2006-01-02")
	for _, snap := range chain {
		if snap.Expiry.Format("2006-01-02") != expDate {
			continue
		}
		if math.Abs(snap.Strike-strike) > 0.005 {
			continue
		}
		return snap, true
	}
	return domain.OptionContractSnapshot{}, false
}
