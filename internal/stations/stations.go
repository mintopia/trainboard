// Package stations provides an offline CRS-code → station-name lookup, backed
// by a bundled snapshot of the UK railway station list. Used by the web UI to
// resolve codes as the user types ("THA · Thatcham").
package stations

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed data/stations.csv
var rawCSV string

var (
	once  sync.Once
	table map[string]string
)

func load() {
	table = make(map[string]string, 2700)
	for _, line := range strings.Split(rawCSV, "\n") {
		crs, name, ok := strings.Cut(strings.TrimRight(line, "\r"), ",")
		if !ok || len(crs) != 3 {
			continue
		}
		table[strings.ToUpper(crs)] = strings.Trim(name, `"`)
	}
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
