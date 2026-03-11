package screener

import "fmt"

func Anonymize(symbols []string) ([]string, map[string]string) {
	anon := make([]string, len(symbols))
	mapping := make(map[string]string, len(symbols))
	for i, sym := range symbols {
		id := anonID(i)
		anon[i] = id
		mapping[id] = sym
	}
	return anon, mapping
}

func Deanonymize(mapping map[string]string, anonID string) (string, bool) {
	sym, ok := mapping[anonID]
	return sym, ok
}

func anonID(index int) string {
	if index < 26 {
		return fmt.Sprintf("ASSET_%c", 'A'+rune(index))
	}
	first := (index-26)/26 + 'A'
	second := (index-26)%26 + 'A'
	return fmt.Sprintf("ASSET_%c%c", rune(first), rune(second))
}
