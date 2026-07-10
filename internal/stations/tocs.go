package stations

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed data/tocs.csv
var rawTOCs string

// TOC is one train operating company: ATOC code → passenger-facing name.
type TOC struct {
	Code string
	Name string
}

var (
	tocOnce  sync.Once
	tocTable map[string]string
	tocList  []TOC
)

func loadTOCs() {
	tocTable = make(map[string]string, 40)
	for _, line := range strings.Split(rawTOCs, "\n") {
		code, name, ok := strings.Cut(strings.TrimRight(line, "\r"), ",")
		if !ok || len(code) != 2 {
			continue
		}
		code = strings.ToUpper(code)
		tocTable[code] = name
		tocList = append(tocList, TOC{Code: code, Name: name})
	}
}

// TOCName returns the operator name for a 2-letter ATOC code
// (case-insensitive).
func TOCName(code string) (string, bool) {
	if len(code) != 2 {
		return "", false
	}
	tocOnce.Do(loadTOCs)
	name, ok := tocTable[strings.ToUpper(code)]
	return name, ok
}

// TOCSearch finds operators by code or name fragment, exact code first.
// An empty query returns the whole table (it is ~31 rows; the web UI
// caches it client-side for name hints).
func TOCSearch(q string, limit int) []TOC {
	tocOnce.Do(loadTOCs)
	q = strings.TrimSpace(q)
	if limit <= 0 {
		return nil
	}
	if q == "" {
		out := tocList
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	uq, lq := strings.ToUpper(q), strings.ToLower(q)
	var exact, rest []TOC
	for _, tc := range tocList {
		switch {
		case tc.Code == uq:
			exact = append(exact, tc)
		case strings.Contains(strings.ToLower(tc.Name), lq):
			rest = append(rest, tc)
		}
	}
	out := append(exact, rest...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
