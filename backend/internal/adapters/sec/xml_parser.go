package sec

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// RawHolding is a single holding row from a 13F informationTable.
type RawHolding struct {
	NameOfIssuer string
	TitleOfClass string
	CUSIP        string
	Value        int64  // market value in thousands
	ShareCount   int64
	ShareType    string // "SH" (shares) or "PRN" (principal)
	PutCall      string // "PUT", "CALL", or ""
}

// xmlInfoTable maps the top-level <informationTable> element.
type xmlInfoTable struct {
	XMLName xml.Name       `xml:"informationTable"`
	Entries []xmlInfoEntry `xml:"infoTable"`
}

// xmlInfoEntry maps a single <infoTable> holding entry.
type xmlInfoEntry struct {
	NameOfIssuer string        `xml:"nameOfIssuer"`
	TitleOfClass string        `xml:"titleOfClass"`
	CUSIP        string        `xml:"cusip"`
	Value        int64         `xml:"value"`
	SharesOrPrn  xmlSharesInfo `xml:"shrsOrPrnAmt"`
	PutCall      string        `xml:"putCall"`
}

// xmlSharesInfo maps the <shrsOrPrnAmt> element.
type xmlSharesInfo struct {
	Amount int64  `xml:"sshPrnamt"`
	Type   string `xml:"sshPrnamtType"`
}

// ParseInformationTable parses SEC 13F informationTable XML into RawHolding slices.
// It handles both bare and namespace-prefixed (ns1:) XML variants.
func ParseInformationTable(data []byte) ([]RawHolding, error) {
	holdings, err := parseInfoTableXML(data)
	if err == nil && len(holdings) > 0 {
		return holdings, nil
	}

	// Retry after stripping common namespace prefixes.
	stripped := stripNamespacePrefixes(data)
	holdings, err = parseInfoTableXML(stripped)
	if err != nil {
		return nil, fmt.Errorf("sec: xml unmarshal: %w", err)
	}

	return holdings, nil
}

func parseInfoTableXML(data []byte) ([]RawHolding, error) {
	var table xmlInfoTable
	if err := xml.Unmarshal(data, &table); err != nil {
		return nil, err
	}

	holdings := make([]RawHolding, 0, len(table.Entries))
	for _, e := range table.Entries {
		holdings = append(holdings, RawHolding{
			NameOfIssuer: e.NameOfIssuer,
			TitleOfClass: e.TitleOfClass,
			CUSIP:        e.CUSIP,
			Value:        e.Value,
			ShareCount:   e.SharesOrPrn.Amount,
			ShareType:    e.SharesOrPrn.Type,
			PutCall:      e.PutCall,
		})
	}
	return holdings, nil
}

// stripNamespacePrefixes removes common XML namespace prefixes like "ns1:" that
// appear in some 13F filings so the standard struct tags can match.
func stripNamespacePrefixes(data []byte) []byte {
	s := string(data)
	// Remove prefixes like ns1:, ns2:, etc. in both opening and closing tags.
	for _, prefix := range []string{"ns1:", "ns2:", "ns3:"} {
		s = strings.ReplaceAll(s, "<"+prefix, "<")
		s = strings.ReplaceAll(s, "</"+prefix, "</")
	}
	return []byte(s)
}
