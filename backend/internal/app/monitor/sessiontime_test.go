package monitor

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestSessionKeyET(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}

	t.Run("before open", func(t *testing.T) {
		et := time.Date(2025, 3, 4, 8, 0, 0, 0, loc)
		require.Equal(t, "2025-03-04", SessionKeyET(et.UTC()))
	})

	t.Run("during session", func(t *testing.T) {
		et := time.Date(2025, 3, 4, 10, 0, 0, 0, loc)
		require.Equal(t, "2025-03-04", SessionKeyET(et.UTC()))
	})

	t.Run("after close", func(t *testing.T) {
		et := time.Date(2025, 3, 4, 18, 0, 0, 0, loc)
		require.Equal(t, "2025-03-04", SessionKeyET(et.UTC()))
	})
}

func TestRTHOpenUTC(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}

	ref := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	openUTC := RTHOpenUTC(ref)
	openET := openUTC.In(loc)
	require.Equal(t, 2025, openET.Year())
	require.Equal(t, time.March, openET.Month())
	require.Equal(t, 4, openET.Day())
	require.Equal(t, 9, openET.Hour())
	require.Equal(t, 30, openET.Minute())
	require.Equal(t, 0, openET.Second())
}

func TestRTHEndUTC_NormalClose(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}

	ref := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	endUTC := RTHEndUTC(ref)
	endET := endUTC.In(loc)
	require.Equal(t, 16, endET.Hour())
	require.Equal(t, 0, endET.Minute())
}

func TestRTHEndUTC_EarlyClose(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}

	ref := time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC)
	endUTC := RTHEndUTC(ref)
	endET := endUTC.In(loc)
	require.Equal(t, 13, endET.Hour())
	require.Equal(t, 0, endET.Minute())
}

func TestIsWithinORBWindow(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}

	window := 30
	require.True(t, IsWithinORBWindow(time.Date(2025, 3, 4, 9, 30, 0, 0, loc).UTC(), window))
	require.True(t, IsWithinORBWindow(time.Date(2025, 3, 4, 9, 59, 59, 0, loc).UTC(), window))
	require.False(t, IsWithinORBWindow(time.Date(2025, 3, 4, 10, 0, 0, 0, loc).UTC(), window))
}

func TestSessionKeyForAsset_Equity(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}
	et := time.Date(2025, 3, 4, 10, 0, 0, 0, loc)
	result := SessionKeyForAsset(et.UTC(), domain.AssetClassEquity)
	require.Equal(t, "2025-03-04", result)
}

func TestSessionKeyForAsset_Crypto(t *testing.T) {
	// 2025-03-04 23:30 UTC → should produce 2025-03-04 (UTC date)
	utc := time.Date(2025, 3, 4, 23, 30, 0, 0, time.UTC)
	result := SessionKeyForAsset(utc, domain.AssetClassCrypto)
	require.Equal(t, "2025-03-04", result)
}

func TestRTHOpenUTCForAsset_Equity(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}
	ref := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	openUTC := RTHOpenUTCForAsset(ref, domain.AssetClassEquity)
	openET := openUTC.In(loc)
	require.Equal(t, 9, openET.Hour())
	require.Equal(t, 30, openET.Minute())
}

func TestRTHOpenUTCForAsset_Crypto(t *testing.T) {
	ref := time.Date(2025, 3, 4, 15, 30, 0, 0, time.UTC)
	openUTC := RTHOpenUTCForAsset(ref, domain.AssetClassCrypto)
	require.Equal(t, 0, openUTC.Hour())
	require.Equal(t, 0, openUTC.Minute())
	require.Equal(t, 0, openUTC.Second())
	require.Equal(t, 2025, openUTC.Year())
	require.Equal(t, time.March, openUTC.Month())
	require.Equal(t, 4, openUTC.Day())
}

func TestRTHEndUTCForAsset_Equity(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}
	ref := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	endUTC := RTHEndUTCForAsset(ref, domain.AssetClassEquity)
	endET := endUTC.In(loc)
	require.Equal(t, 16, endET.Hour())
	require.Equal(t, 0, endET.Minute())
}

func TestRTHEndUTCForAsset_Crypto(t *testing.T) {
	ref := time.Date(2025, 3, 4, 15, 30, 0, 0, time.UTC)
	endUTC := RTHEndUTCForAsset(ref, domain.AssetClassCrypto)
	require.Equal(t, 23, endUTC.Hour())
	require.Equal(t, 59, endUTC.Minute())
	require.Equal(t, 59, endUTC.Second())
	require.Equal(t, 2025, endUTC.Year())
	require.Equal(t, time.March, endUTC.Month())
	require.Equal(t, 4, endUTC.Day())
}
