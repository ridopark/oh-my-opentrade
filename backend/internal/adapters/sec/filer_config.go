package sec

// FilerConfig defines a tracked institutional filer.
type FilerConfig struct {
	CIK  string
	Name string
	Tier int // 1=high-conviction stock-pickers, 2=quant/multi-strategy
}

// DefaultFilers returns the default list of tracked 13F filers.
// CIK numbers are sourced from SEC EDGAR. Tier 1 filers are concentrated
// stock-pickers with the highest alpha signal; Tier 2 are quant/multi-strategy
// firms useful for crowding detection.
func DefaultFilers() []FilerConfig {
	return []FilerConfig{
		// Tier 1: Concentrated stock-pickers (highest alpha signal)
		{CIK: "1067983", Name: "Berkshire Hathaway", Tier: 1},
		{CIK: "1656456", Name: "Appaloosa Management", Tier: 1},
		{CIK: "1336528", Name: "Pershing Square Capital", Tier: 1},
		{CIK: "1079114", Name: "Greenlight Capital", Tier: 1},
		{CIK: "1061768", Name: "Baupost Group", Tier: 1},
		{CIK: "1345471", Name: "Icahn Enterprises", Tier: 1},
		{CIK: "1649339", Name: "Duquesne Family Office", Tier: 1},
		{CIK: "1103804", Name: "ValueAct Capital", Tier: 1},
		{CIK: "1159159", Name: "Third Point", Tier: 1},
		{CIK: "1040273", Name: "Viking Global Investors", Tier: 1},
		{CIK: "1484148", Name: "Coatue Management", Tier: 1},
		{CIK: "1167557", Name: "Lone Pine Capital", Tier: 1},
		{CIK: "1029160", Name: "Soros Fund Management", Tier: 1},
		{CIK: "1535392", Name: "Tiger Global Management", Tier: 1},
		{CIK: "1697748", Name: "Point72 Asset Management", Tier: 1},

		// Tier 2: Quant / multi-strategy (useful for crowding signal)
		{CIK: "1037389", Name: "Renaissance Technologies", Tier: 2},
		{CIK: "1423053", Name: "Citadel Advisors", Tier: 2},
		{CIK: "1061165", Name: "Two Sigma Investments", Tier: 2},
		{CIK: "1350694", Name: "AQR Capital Management", Tier: 2},
		{CIK: "1364742", Name: "DE Shaw & Co", Tier: 2},
		{CIK: "1336326", Name: "Bridgewater Associates", Tier: 2},
		{CIK: "1273087", Name: "Millennium Management", Tier: 2},
		{CIK: "1479844", Name: "Jane Street Group", Tier: 2},
	}
}
