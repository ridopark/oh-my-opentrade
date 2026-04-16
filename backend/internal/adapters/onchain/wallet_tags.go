package onchain

// WalletTag categorizes a known wallet address.
type WalletTag struct {
	Address  string
	Tag      string // e.g. "blackrock_custody", "fidelity_custody", "binance_hot"
	Entity   string // e.g. "BlackRock", "Fidelity", "Binance"
	Category string // "etf_custodian", "exchange_hot", "exchange_cold", "market_maker"
}

// CategoryETFCustodian tags wallets used by ETF issuers for custody.
const CategoryETFCustodian = "etf_custodian"

// CategoryExchangeHot tags exchange hot wallets (active trading, deposits).
const CategoryExchangeHot = "exchange_hot"

// CategoryExchangeCold tags exchange cold storage wallets.
const CategoryExchangeCold = "exchange_cold"

// CategoryMarketMaker tags known market maker wallets.
const CategoryMarketMaker = "market_maker"

// IsExchange returns true if the wallet belongs to an exchange (hot or cold).
func (wt WalletTag) IsExchange() bool {
	return wt.Category == CategoryExchangeHot || wt.Category == CategoryExchangeCold
}

// DefaultWalletTags returns the curated set of tagged addresses. These are
// well-known public addresses for major entities. Placeholder addresses are
// used here — production deployments should override via YAML config.
//
// Address sources: on-chain analytics (Arkham, Nansen), public disclosures.
// NOTE: These are EXAMPLE addresses for development. Real production addresses
// should be loaded from configuration.
func DefaultWalletTags() map[string]WalletTag {
	tags := []WalletTag{
		// ETF Custodians — Coinbase Prime custody for BlackRock/iShares IBIT
		{Address: "0x example_blackrock_btc_custody_1", Tag: "blackrock_custody", Entity: "BlackRock", Category: CategoryETFCustodian},
		{Address: "0x example_blackrock_eth_custody_1", Tag: "blackrock_custody", Entity: "BlackRock", Category: CategoryETFCustodian},

		// Fidelity FBTC custody (self-custodied via Fidelity Digital Assets)
		{Address: "0x example_fidelity_btc_custody_1", Tag: "fidelity_custody", Entity: "Fidelity", Category: CategoryETFCustodian},

		// Grayscale — GBTC/ETHE custody
		{Address: "0x example_grayscale_btc_custody_1", Tag: "grayscale_custody", Entity: "Grayscale", Category: CategoryETFCustodian},
		{Address: "0x example_grayscale_eth_custody_1", Tag: "grayscale_custody", Entity: "Grayscale", Category: CategoryETFCustodian},

		// Binance hot wallets
		{Address: "0x example_binance_hot_1", Tag: "binance_hot", Entity: "Binance", Category: CategoryExchangeHot},
		{Address: "0x example_binance_hot_2", Tag: "binance_hot", Entity: "Binance", Category: CategoryExchangeHot},

		// Coinbase hot wallets
		{Address: "0x example_coinbase_hot_1", Tag: "coinbase_hot", Entity: "Coinbase", Category: CategoryExchangeHot},
		{Address: "0x example_coinbase_hot_2", Tag: "coinbase_hot", Entity: "Coinbase", Category: CategoryExchangeHot},

		// Kraken hot wallets
		{Address: "0x example_kraken_hot_1", Tag: "kraken_hot", Entity: "Kraken", Category: CategoryExchangeHot},

		// Exchange cold storage
		{Address: "0x example_binance_cold_1", Tag: "binance_cold", Entity: "Binance", Category: CategoryExchangeCold},
		{Address: "0x example_coinbase_cold_1", Tag: "coinbase_cold", Entity: "Coinbase", Category: CategoryExchangeCold},

		// Market makers
		{Address: "0x example_wintermute_1", Tag: "wintermute", Entity: "Wintermute", Category: CategoryMarketMaker},
		{Address: "0x example_jump_1", Tag: "jump_trading", Entity: "Jump Trading", Category: CategoryMarketMaker},
	}

	m := make(map[string]WalletTag, len(tags))
	for _, t := range tags {
		m[t.Address] = t
	}
	return m
}

// MergeWalletTags merges additional tags into the base set. Entries in
// additional override entries in base for the same address.
func MergeWalletTags(base, additional map[string]WalletTag) map[string]WalletTag {
	merged := make(map[string]WalletTag, len(base)+len(additional))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range additional {
		merged[k] = v
	}
	return merged
}
