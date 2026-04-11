package timescaledb

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"strings"
)

// stringArray is a minimal sql.Scanner + driver.Valuer for Postgres text[]
// columns. Equivalent to lib/pq.StringArray but we implement it locally to
// avoid taking a dependency on lib/pq when the rest of the codebase uses
// pgx/v5 stdlib. Handles the literal form `{a,b,"quoted, with comma"}`.
type stringArray []string

// Value produces the Postgres array literal wire format.
func (a stringArray) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	if len(a) == 0 {
		return "{}", nil
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, s := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range s {
			switch r {
			case '\\', '"':
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), nil
}

// Scan parses a Postgres array literal into a []string.
func (a *stringArray) Scan(src any) error {
	if src == nil {
		*a = nil
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("stringArray: unsupported scan type %T", src)
	}
	parsed, err := parsePGStringArray(s)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// parsePGStringArray parses the `{a,"b, c",d}` literal format.
func parsePGStringArray(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return []string{}, nil
	}
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("stringArray: invalid literal %q", s)
	}
	body := s[1 : len(s)-1]

	var (
		out     []string
		cur     bytes.Buffer
		inQuote bool
		i       int
	)
	for i < len(body) {
		c := body[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(body):
			cur.WriteByte(body[i+1])
			i += 2
		case c == '"':
			inQuote = !inQuote
			i++
		case !inQuote && c == ',':
			out = append(out, cur.String())
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	// Emit final element. Treat unquoted "NULL" as empty string (Postgres
	// writes NULL without quotes); for our columns (NOT NULL tags/strategies/
	// symbols), this branch shouldn't trigger in practice.
	last := cur.String()
	if last == "NULL" {
		last = ""
	}
	out = append(out, last)
	return out, nil
}
