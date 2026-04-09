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

// xmlInfoTableNS maps with the SEC default namespace.
type xmlInfoTableNS struct {
	XMLName xml.Name         `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable informationTable"`
	Entries []xmlInfoEntryNS `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable infoTable"`
}

// xmlInfoEntry maps a single <infoTable> holding entry (no namespace).
type xmlInfoEntry struct {
	NameOfIssuer string        `xml:"nameOfIssuer"`
	TitleOfClass string        `xml:"titleOfClass"`
	CUSIP        string        `xml:"cusip"`
	Value        int64         `xml:"value"`
	SharesOrPrn  xmlSharesInfo `xml:"shrsOrPrnAmt"`
	PutCall      string        `xml:"putCall"`
}

// xmlInfoEntryNS maps with SEC default namespace.
type xmlInfoEntryNS struct {
	NameOfIssuer string          `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable nameOfIssuer"`
	TitleOfClass string          `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable titleOfClass"`
	CUSIP        string          `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable cusip"`
	Value        int64           `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable value"`
	SharesOrPrn  xmlSharesInfoNS `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable shrsOrPrnAmt"`
	PutCall      string          `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable putCall"`
}

// xmlSharesInfo maps the <shrsOrPrnAmt> element.
type xmlSharesInfo struct {
	Amount int64  `xml:"sshPrnamt"`
	Type   string `xml:"sshPrnamtType"`
}

type xmlSharesInfoNS struct {
	Amount int64  `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable sshPrnamt"`
	Type   string `xml:"http://www.sec.gov/edgar/document/thirteenf/informationtable sshPrnamtType"`
}

// ParseInformationTable parses SEC 13F informationTable XML into RawHolding slices.
// Handles three XML variants: default SEC namespace, ns1: prefixes, and bare elements.
func ParseInformationTable(data []byte) ([]RawHolding, error) {
	// Try 1: SEC default namespace (most common modern format).
	var tableNS xmlInfoTableNS
	if err := xml.Unmarshal(data, &tableNS); err == nil && len(tableNS.Entries) > 0 {
		holdings := make([]RawHolding, 0, len(tableNS.Entries))
		for _, e := range tableNS.Entries {
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

	// Try 2: bare elements (no namespace).
	var table xmlInfoTable
	if err := xml.Unmarshal(data, &table); err == nil && len(table.Entries) > 0 {
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

	// Try 3: strip namespace prefixes (ns1:, ns2:) and retry bare.
	stripped := stripNamespacePrefixes(data)
	if err := xml.Unmarshal(stripped, &table); err != nil {
		return nil, fmt.Errorf("sec: xml unmarshal: %w", err)
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
