package builtin

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// hvnTracker is a session-rolling volume-profile collaborator shared by
// avwap_v1 (diagnostic-only) and whale_pullback_v1 (vp_required veto).
// Math is identical to the pre-extraction free functions; the invariants
// callers must hold are: Reset(binBps) before the first Update, and the
// same (binBps, lookbackDays, thresholdPct, rthOnly, tz) on every Update.
type hvnTracker struct {
	sessions    []*sessionHist
	merged      map[int]float64
	hvnSet      map[int]struct{}
	anchor      float64
	binBps      float64
	sessionDate string
}

// Reset zeroes all session state and seeds binBps. The seed is required so
// diag-tag emitters reading BinBps() between Init prior-restore and the
// first Update see the configured value rather than zero.
func (t *hvnTracker) Reset(binBps float64) {
	t.sessions = nil
	t.merged = nil
	t.hvnSet = nil
	t.anchor = 0
	t.binBps = binBps
	t.sessionDate = ""
}

func (t *hvnTracker) Update(bar start.Bar, lookbackDays int, binBps, thresholdPct float64, rthOnly bool, tz string) {
	loc := cachedLocation(tz)
	if loc == nil {
		loc = etLocation
	}
	dateStr := bar.Time.In(loc).Format("2006-01-02")
	if t.sessionDate != dateStr {
		t.sessionDate = dateStr
		anchor := bar.Close
		t.sessions = append(t.sessions, &sessionHist{
			hist:   start.NewVolumeHistogram(binBps, anchor),
			anchor: anchor,
		})
		for len(t.sessions) > lookbackDays {
			t.sessions = t.sessions[1:]
		}
		t.rebuildMerged(lookbackDays, binBps, thresholdPct)
	}
	if len(t.sessions) == 0 {
		return
	}
	if rthOnly && !isRTHBar(bar.Time, tz) {
		return
	}
	cur := t.sessions[len(t.sessions)-1]
	cur.hist.Accumulate(bar)
}

// rebuildMerged anchors on the oldest kept prior session, re-keys all kept
// prior sessions under that anchor, and derives the HVN bin set at
// thresholdPct. Sets merged/hvnSet/anchor to nil/nil/0 when fewer than 2
// sessions are ingested -- whale's vp_required first-day veto reads
// `merged == nil` and that contract must be preserved.
func (t *hvnTracker) rebuildMerged(lookbackDays int, binBps, thresholdPct float64) {
	keep := t.sessions
	if len(keep) <= 1 {
		t.merged = nil
		t.hvnSet = nil
		t.anchor = 0
		return
	}
	prior := keep[:len(keep)-1]
	if len(prior) > lookbackDays {
		prior = prior[len(prior)-lookbackDays:]
	}
	anchor := prior[0].anchor
	t.anchor = anchor

	merged := start.NewVolumeHistogram(binBps, anchor)
	for _, sess := range prior {
		if sess.hist == nil {
			continue
		}
		for oldIdx, v := range sess.hist.Bins() {
			price := sess.hist.BinCenter(oldIdx)
			newIdx := merged.BinIndex(price)
			merged.Bins()[newIdx] += v
		}
	}
	t.merged = merged.Bins()

	hvnIdx := merged.HVNBins(thresholdPct)
	t.hvnSet = make(map[int]struct{}, len(hvnIdx))
	for _, idx := range hvnIdx {
		t.hvnSet[idx] = struct{}{}
	}
}

// HVNContainsPrice reports whether any HVN bin overlaps the inclusive
// price range [low, high]. Used by whale's vetoByVP span check and by
// parity tests that assert window-roll re-keying behavior.
func (t *hvnTracker) HVNContainsPrice(low, high float64) bool {
	if len(t.hvnSet) == 0 || t.binBps <= 0 {
		return false
	}
	tmp := start.NewVolumeHistogram(t.binBps, t.anchor)
	loIdx := tmp.BinIndex(low)
	hiIdx := tmp.BinIndex(high)
	for idx := range t.hvnSet {
		if idx >= loIdx && idx <= hiIdx {
			return true
		}
	}
	return false
}

func (t *hvnTracker) Fingerprint() string { return fingerprintBinSet(t.hvnSet) }

// Merged is nil iff fewer than 2 sessions ingested -- callers depend on
// the nil/non-nil distinction (whale's vp_required first-day veto).
func (t *hvnTracker) Merged() map[int]float64  { return t.merged }
func (t *hvnTracker) HVNSet() map[int]struct{} { return t.hvnSet }
func (t *hvnTracker) Anchor() float64          { return t.anchor }
func (t *hvnTracker) BinBps() float64          { return t.binBps }

func fingerprintBinSet(set map[int]struct{}) string {
	if len(set) == 0 {
		return ""
	}
	keys := make([]int, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = h.Write([]byte(strconv.Itoa(k)))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
