package screener

import (
	"errors"
	"math"
	"strings"
	"time"
)

type DataStatus string

const (
	DataStatusOK          DataStatus = "ok"
	DataStatusMissingData DataStatus = "missing_data"
	DataStatusError       DataStatus = "error"
)

type PriceSource string

const (
	PriceSourcePreMarket PriceSource = "pre_market"
	PriceSourceLastTrade PriceSource = "last_trade"
)

type ScreenerScore struct {
	GapScore  float64
	RVOLScore float64
	NewsScore *float64
	Total     float64
}

func NormalizeGap(gapPct float64) float64 {
	v := gapPct / 10.0
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

func NormalizeRVOL(rvol float64) float64 {
	if math.IsNaN(rvol) || math.IsInf(rvol, 0) {
		return 0
	}
	v := (rvol - 1.0) / 2.0
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

type ScreenerResult struct {
	TenantID        string
	EnvMode         string
	RunID           string
	AsOf            time.Time
	Symbol          string
	PrevClose       *float64
	PreMarketPrice  *float64
	PreMarketVolume *int64
	AvgHistVolume   *int64
	GapPct          *float64
	RVOL            *float64
	Score           ScreenerScore
	Status          DataStatus
	PriceSource     *PriceSource
	ErrorMsg        *string
	CreatedAt       time.Time
}

func (r ScreenerResult) Validate() error {
	if strings.TrimSpace(r.TenantID) == "" {
		return errors.New("tenant id is required")
	}
	if strings.TrimSpace(r.EnvMode) == "" {
		return errors.New("env mode is required")
	}
	if strings.TrimSpace(r.RunID) == "" {
		return errors.New("run id is required")
	}
	if r.AsOf.IsZero() {
		return errors.New("as of is required")
	}
	if strings.TrimSpace(r.Symbol) == "" {
		return errors.New("symbol is required")
	}
	if strings.TrimSpace(string(r.Status)) == "" {
		return errors.New("status is required")
	}
	return nil
}
