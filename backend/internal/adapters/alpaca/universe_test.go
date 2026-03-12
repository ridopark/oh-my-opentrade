package alpaca

import (
	"testing"
)

func TestIsScreenableEquity(t *testing.T) {
	cases := []struct {
		name         string
		symbol       string
		assetName    string
		exchange     string
		shortable    bool
		marginable   bool
		fractionable bool
		want         bool
	}{
		// Normal equities — should pass
		{"common stock", "AAPL", "Apple Inc. Common Stock", "NASDAQ", true, true, true, true},
		{"NYSE stock", "GS", "Goldman Sachs Group Inc.", "NYSE", true, true, true, true},
		{"BATS stock", "SPY", "SPDR S&P 500 ETF Trust", "BATS", true, true, true, false}, // fractionable=false → reject
		// ETFs — should be filtered
		{"etf name suffix", "IEFA", "iShares Core MSCI EAFE ETF", "BATS", true, true, true, false},
		{"etf name suffix 2", "VCSH", "Vanguard Short-Term Corporate Bond ETF", "NASDAQ", true, true, true, false},
		{"etf spdr", "SPIB", "State Street SPDR Portfolio Intermediate Term Corporate Bond ETF", "BATS", true, true, true, false},
		{"etf capital group", "CGGR", "Capital Group Growth ETF", "NASDAQ", true, true, true, false},
		{"etf ishares tech", "IYW", "iShares U.S. Technology ETF", "BATS", true, true, true, false},
		{"etf vanguard funds", "BNDX", "Vanguard Charlotte Funds Vanguard Total International Bond ETF", "NASDAQ", true, true, true, false},
		// Junk — should be filtered
		{"warrant", "SPCE+", "Virgin Galactic Holdings Inc. Warrant", "NYSE", true, true, true, false},
		{"spac", "ACAAU", "Acamar Partners Acquisition Corp II Unit", "NASDAQ", true, true, true, false},
		{"preferred", "BAC-L", "Bank of America Corp Preferred", "NYSE", true, true, true, false},
		{"depositary", "BABA", "Alibaba Group Depositary Receipt", "NYSE", true, true, true, false},
		// Non-screener criteria
		{"not shortable or marginable", "XYZ", "Some Corp", "NYSE", false, false, true, false},
		{"not fractionable", "XYZ", "Some Corp", "NYSE", true, true, false, false},
		{"bad exchange", "XYZ", "Some Corp", "OTC", true, true, true, false},
		{"symbol with space", "BRK A", "Berkshire Hathaway Inc.", "NYSE", true, true, true, false},
		{"symbol with dash", "BRK-A", "Berkshire Hathaway Inc.", "NYSE", true, true, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isScreenableEquity(tc.exchange, tc.symbol, tc.assetName, tc.shortable, tc.marginable, tc.fractionable)
			if got != tc.want {
				t.Errorf("isScreenableEquity(%q, %q, %q, shortable=%v, marginable=%v, fractionable=%v) = %v, want %v",
					tc.exchange, tc.symbol, tc.assetName, tc.shortable, tc.marginable, tc.fractionable, got, tc.want)
			}
		})
	}
}
