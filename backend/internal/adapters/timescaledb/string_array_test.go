package timescaledb

import (
	"reflect"
	"testing"
)

func TestStringArrayValueScanRoundTrip(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"AVWAP"},
		{"AVWAP", "ORB"},
		{"baseline", "prod-candidate"},
		{`quoted, with comma`, `has "quote"`, `back\slash`},
	}
	for _, in := range cases {
		var src stringArray = in
		v, err := src.Value()
		if err != nil {
			t.Fatalf("Value(%v) error: %v", in, err)
		}
		if v == nil {
			if in != nil {
				t.Fatalf("Value(%v) returned nil", in)
			}
			continue
		}
		var dst stringArray
		if err := dst.Scan(v); err != nil {
			t.Fatalf("Scan(%q) error: %v", v, err)
		}
		want := in
		if want == nil {
			want = []string{}
		}
		if !reflect.DeepEqual([]string(dst), want) {
			t.Fatalf("roundtrip mismatch: in=%v out=%v raw=%q", in, dst, v)
		}
	}
}

func TestStringArrayScanNil(t *testing.T) {
	var a stringArray = []string{"pre-existing"}
	if err := a.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if a != nil {
		t.Fatalf("expected nil, got %v", a)
	}
}
