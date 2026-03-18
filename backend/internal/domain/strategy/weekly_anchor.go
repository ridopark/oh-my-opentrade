package strategy

import (
	"time"
)

var etLocation_ *time.Location

func init() {
	var err error
	etLocation_, err = time.LoadLocation("America/New_York")
	if err != nil {
		panic("failed to load America/New_York timezone: " + err.Error())
	}
}

// WeeklyAnchorDetector emits a CandidateAnchor on the first bar of each
// new ISO trading week. For equity, anchors trigger on Monday >= 09:30 ET.
// For crypto, anchors trigger on Monday >= 00:00 UTC.
type WeeklyAnchorDetector struct {
	isCrypto    bool
	timeframe   string
	lastISOYear int
	lastISOWeek int
	initialized bool
}

func NewWeeklyAnchorDetector(isCrypto bool, timeframe string) *WeeklyAnchorDetector {
	return &WeeklyAnchorDetector{
		isCrypto:  isCrypto,
		timeframe: timeframe,
	}
}

func (d *WeeklyAnchorDetector) Push(bar Bar) *CandidateAnchor {
	var refTime time.Time
	if d.isCrypto {
		refTime = bar.Time.UTC()
	} else {
		refTime = bar.Time.In(etLocation_)
	}

	if !d.isCrypto && refTime.Weekday() != time.Monday {
		if d.initialized {
			return nil
		}
	}

	if !d.isCrypto {
		mondayOpen := time.Date(refTime.Year(), refTime.Month(), refTime.Day(), 9, 30, 0, 0, etLocation_)
		if refTime.Before(mondayOpen) {
			return nil
		}
	}

	isoYear, isoWeek := refTime.ISOWeek()

	if d.initialized && isoYear == d.lastISOYear && isoWeek == d.lastISOWeek {
		return nil
	}

	if !d.initialized && !d.isCrypto && refTime.Weekday() != time.Monday {
		d.initialized = true
		d.lastISOYear = isoYear
		d.lastISOWeek = isoWeek
		return nil
	}

	d.initialized = true
	d.lastISOYear = isoYear
	d.lastISOWeek = isoWeek

	ca, err := NewCandidateAnchor(bar.Time, bar.Open, AnchorWeeklyOpen, d.timeframe, 1.0)
	if err != nil {
		return nil
	}
	return &ca
}
