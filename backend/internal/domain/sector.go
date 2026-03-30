package domain

// SectorGroup classifies symbols into correlation groups for portfolio risk management.
type SectorGroup string

const (
	SectorTech        SectorGroup = "TECH"
	SectorSemis       SectorGroup = "SEMIS"
	SectorSoftware    SectorGroup = "SOFTWARE"
	SectorFintech     SectorGroup = "FINTECH"
	SectorFinancial   SectorGroup = "FINANCIAL"
	SectorEnergy      SectorGroup = "ENERGY"
	SectorHealth      SectorGroup = "HEALTH"
	SectorConsumer    SectorGroup = "CONSUMER"
	SectorIndustrial  SectorGroup = "INDUSTRIAL"
	SectorETF         SectorGroup = "ETF"
	SectorLevETF      SectorGroup = "LEV_ETF"
	SectorCryptoProxy SectorGroup = "CRYPTO_PROXY"
	SectorOther       SectorGroup = "OTHER"
)

var symbolSector = map[string]SectorGroup{
	// Mega cap tech
	"AAPL": SectorTech, "MSFT": SectorTech, "GOOGL": SectorTech,
	"AMZN": SectorTech, "META": SectorTech, "TSLA": SectorTech, "NFLX": SectorTech,

	// Semiconductors
	"NVDA": SectorSemis, "AMD": SectorSemis, "INTC": SectorSemis,
	"AVGO": SectorSemis, "QCOM": SectorSemis, "MU": SectorSemis,
	"MRVL": SectorSemis, "ON": SectorSemis, "SMCI": SectorSemis,

	// Software / Cloud
	"CRM": SectorSoftware, "ORCL": SectorSoftware, "SNOW": SectorSoftware,
	"PLTR": SectorSoftware, "U": SectorSoftware, "NET": SectorSoftware,
	"DDOG": SectorSoftware, "ZS": SectorSoftware,

	// Fintech
	"SOFI": SectorFintech, "COIN": SectorFintech, "HOOD": SectorFintech,
	"SQ": SectorFintech, "PYPL": SectorFintech, "AFRM": SectorFintech,
	"UPST": SectorFintech,

	// Traditional finance
	"V": SectorFinancial, "MA": SectorFinancial, "BAC": SectorFinancial,
	"JPM": SectorFinancial, "GS": SectorFinancial,

	// Energy
	"XOM": SectorEnergy, "CVX": SectorEnergy, "OXY": SectorEnergy, "SLB": SectorEnergy,

	// Healthcare / Biotech
	"MRNA": SectorHealth, "PFE": SectorHealth, "ABBV": SectorHealth,
	"LLY": SectorHealth, "UNH": SectorHealth, "JNJ": SectorHealth,

	// Consumer / Retail / EV
	"HIMS": SectorConsumer, "WMT": SectorConsumer, "COST": SectorConsumer,
	"TGT": SectorConsumer, "RIVN": SectorConsumer, "LCID": SectorConsumer,
	"NIO": SectorConsumer, "F": SectorConsumer, "GM": SectorConsumer,
	"RBLX": SectorConsumer, "FUBO": SectorConsumer,

	// Industrials
	"BA": SectorIndustrial, "CAT": SectorIndustrial, "DE": SectorIndustrial, "UPS": SectorIndustrial,

	// Broad ETFs
	"SPY": SectorETF, "QQQ": SectorETF, "IWM": SectorETF, "DIA": SectorETF,
	"XLF": SectorETF, "XLE": SectorETF, "XLK": SectorETF,

	// Leveraged ETFs
	"SOXL": SectorLevETF, "TQQQ": SectorLevETF, "SQQQ": SectorLevETF,

	// Crypto proxies
	"MARA": SectorCryptoProxy, "RIOT": SectorCryptoProxy,
}

// ClassifySector returns the sector group for a symbol.
// Unknown symbols return SectorOther.
func ClassifySector(sym Symbol) SectorGroup {
	if g, ok := symbolSector[string(sym)]; ok {
		return g
	}
	return SectorOther
}

// KnownSymbols returns all symbols in the sector classification map.
// This is the canonical universe of liquid US equities for screening.
func KnownSymbols() []string {
	syms := make([]string, 0, len(symbolSector))
	for s := range symbolSector {
		syms = append(syms, s)
	}
	return syms
}
