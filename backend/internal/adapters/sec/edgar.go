// Package sec provides an HTTP client for the SEC EDGAR API to fetch
// 13F-HR institutional holdings filings for whale accumulation tracking.
package sec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

// FilingEntry represents a single 13F-HR filing found in the EDGAR index.
type FilingEntry struct {
	AccessionNumber string
	FilingDate      time.Time
	FormType        string // "13F-HR" or "13F-HR/A"
}

// EdgarClient fetches 13F-HR filings from SEC EDGAR.
type EdgarClient struct {
	baseURL   string
	userAgent string
	limiter   *rate.Limiter
	client    *http.Client
	log       zerolog.Logger
}

// NewEdgarClient creates a new SEC EDGAR API client.
// userAgent must identify the caller per SEC fair-access policy
// (e.g. "MyApp admin@example.com").
func NewEdgarClient(userAgent string, log zerolog.Logger) *EdgarClient {
	return &EdgarClient{
		baseURL:   "https://efts.sec.gov/LATEST",
		userAgent: userAgent,
		limiter:   rate.NewLimiter(rate.Limit(10), 1), // 10 req/sec per SEC guidelines
		client:    &http.Client{Timeout: 30 * time.Second},
		log:       log.With().Str("component", "sec-edgar").Logger(),
	}
}

// eftsResponse is the top-level JSON envelope from the EDGAR EFTS search API.
type eftsResponse struct {
	Hits eftsHits `json:"hits"`
}

type eftsHits struct {
	Hits []eftsHit `json:"hits"`
}

type eftsHit struct {
	Source eftsSource `json:"_source"`
}

type eftsSource struct {
	Form        string `json:"form"`          // "13F-HR" or "13F-HR/A"
	FileType    string `json:"file_type"`     // same as form
	FileDate    string `json:"file_date"`     // "2024-05-15"
	PeriodEnd   string `json:"period_ending"` // "2024-03-31"
	AccessionNo string `json:"adsh"`          // "0000950123-24-005653"
}

// FetchFilingIndex retrieves 13F-HR filing entries for a given CIK within a date range.
func (c *EdgarClient) FetchFilingIndex(ctx context.Context, cik string, from, to time.Time) ([]FilingEntry, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("sec: rate limiter: %w", err)
	}

	// EDGAR EFTS requires 10-digit zero-padded CIK.
	paddedCIK := fmt.Sprintf("%010s", cik)
	params := url.Values{
		"q":         {`"13F-HR"`},
		"dateRange": {"custom"},
		"startdt":   {from.Format("2006-01-02")},
		"enddt":     {to.Format("2006-01-02")},
		"forms":     {"13F-HR"},
		"ciks":      {paddedCIK},
	}
	u := c.baseURL + "/search-index?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("sec: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sec: fetch filing index for CIK %s: %w", cik, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sec: EDGAR HTTP %d for CIK %s: %s", resp.StatusCode, cik, string(body))
	}

	var result eftsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("sec: decode filing index JSON: %w", err)
	}

	entries := make([]FilingEntry, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		filingDate, err := time.Parse("2006-01-02", hit.Source.FileDate)
		if err != nil {
			c.log.Warn().Str("cik", cik).Str("date", hit.Source.FileDate).Msg("skipping filing with unparseable date")
			continue
		}
		entries = append(entries, FilingEntry{
			AccessionNumber: hit.Source.AccessionNo,
			FilingDate:      filingDate,
			FormType:        hit.Source.Form,
		})
	}

	c.log.Debug().Str("cik", cik).Int("count", len(entries)).Msg("fetched filing index")
	return entries, nil
}

// FetchInformationTable downloads and parses the 13F information table for a filing.
// It discovers the XML filename from the filing's index.json since names vary per filer.
func (c *EdgarClient) FetchInformationTable(ctx context.Context, cik, accessionNumber string) ([]RawHolding, error) {
	accNoDashes := strings.ReplaceAll(accessionNumber, "-", "")
	baseURL := fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%s/%s", cik, accNoDashes)

	// Step 1: Fetch index.json to find the information table XML filename.
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("sec: rate limiter: %w", err)
	}

	indexURL := baseURL + "/index.json"
	indexReq, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("sec: build index request: %w", err)
	}
	indexReq.Header.Set("User-Agent", c.userAgent)

	indexResp, err := c.client.Do(indexReq)
	if err != nil {
		return nil, fmt.Errorf("sec: fetch index for %s/%s: %w", cik, accessionNumber, err)
	}
	defer indexResp.Body.Close()

	if indexResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(indexResp.Body)
		return nil, fmt.Errorf("sec: index HTTP %d for %s/%s: %s", indexResp.StatusCode, cik, accessionNumber, string(body))
	}

	var indexData struct {
		Directory struct {
			Item []struct {
				Name string `json:"name"`
			} `json:"item"`
		} `json:"directory"`
	}
	if err := json.NewDecoder(indexResp.Body).Decode(&indexData); err != nil {
		return nil, fmt.Errorf("sec: decode index JSON: %w", err)
	}

	// Find the information table XML: any .xml that is not primary_doc.xml and not an index file.
	var infoTableFile string
	for _, item := range indexData.Directory.Item {
		name := item.Name
		if strings.HasSuffix(name, ".xml") && name != "primary_doc.xml" && !strings.Contains(name, "index") {
			infoTableFile = name
			break
		}
	}
	if infoTableFile == "" {
		return nil, fmt.Errorf("sec: no information table XML found in %s/%s", cik, accessionNumber)
	}

	// Step 2: Fetch and parse the information table.
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("sec: rate limiter: %w", err)
	}

	u := baseURL + "/" + infoTableFile
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("sec: build infotable request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sec: fetch infotable for %s/%s: %w", cik, accessionNumber, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sec: infotable HTTP %d for %s/%s: %s", resp.StatusCode, cik, accessionNumber, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sec: read infotable body: %w", err)
	}

	holdings, err := ParseInformationTable(data)
	if err != nil {
		return nil, fmt.Errorf("sec: parse infotable for %s/%s: %w", cik, accessionNumber, err)
	}

	c.log.Debug().Str("cik", cik).Str("accession", accessionNumber).Str("file", infoTableFile).Int("holdings", len(holdings)).Msg("parsed information table")
	return holdings, nil
}
