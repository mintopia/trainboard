// Package stations provides an offline CRS-code → station-name lookup, backed
// by a bundled snapshot of the UK railway station list. Used by the web UI to
// resolve codes as the user types ("THA · Thatcham").
package stations

import (
	_ "embed"
	"sort"
	"strings"
	"sync"
)

//go:embed data/stations.csv
var rawCSV string

// Station is one row of the bundled UK station list.
type Station struct {
	CRS  string
	Name string
}

var (
	once  sync.Once
	table map[string]string
	list  []Station // name-sorted at load (CSV is CRS-sorted; names track closely)
)

func load() {
	table = make(map[string]string, 2700)
	for _, line := range strings.Split(rawCSV, "\n") {
		crs, name, ok := strings.Cut(strings.TrimRight(line, "\r"), ",")
		if !ok || len(crs) != 3 {
			continue
		}
		crs = strings.ToUpper(crs)
		name = strings.Trim(name, `"`)
		table[crs] = name
		list = append(list, Station{CRS: crs, Name: name})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
}

// Name returns the station name for a 3-letter CRS code (case-insensitive).
func Name(crs string) (string, bool) {
	if len(crs) != 3 {
		return "", false
	}
	once.Do(load)
	name, ok := table[strings.ToUpper(crs)]
	return name, ok
}

// Search finds stations whose name or CRS code matches q, best first:
// exact code, then name prefix, then substring. Queries under 2 characters
// return nothing (too noisy to suggest). Case-insensitive.
func Search(q string, limit int) []Station {
	q = strings.TrimSpace(q)
	if len(q) < 2 || limit <= 0 {
		return nil
	}
	once.Do(load)
	uq, lq := strings.ToUpper(q), strings.ToLower(q)

	var exact, prefix, sub []Station
	if name, ok := table[uq]; ok && len(uq) == 3 {
		exact = append(exact, Station{CRS: uq, Name: name})
	}
	for _, s := range list {
		ln := strings.ToLower(s.Name)
		switch {
		case s.CRS == uq:
			// already in exact
		case strings.HasPrefix(ln, lq):
			prefix = append(prefix, s)
		case strings.Contains(ln, lq):
			sub = append(sub, s)
		}
		if len(prefix) >= limit && len(sub) >= limit {
			break
		}
	}
	out := append(append(exact, prefix...), sub...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
