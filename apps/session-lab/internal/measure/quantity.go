package measure

import (
	"strconv"
	"strings"
)

// parseQuantityMilli converts a Kubernetes quantity to milli-units, which is how
// the metrics API reports CPU ("12m", "1", "1500m").
func parseQuantityMilli(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if strings.HasSuffix(raw, "m") {
		value, err := strconv.ParseFloat(strings.TrimSuffix(raw, "m"), 64)
		if err != nil {
			return 0
		}
		return int64(value)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int64(value * 1000)
}

// binary and decimal suffixes the metrics API uses for memory.
var byteSuffixes = []struct {
	suffix string
	scale  float64
}{
	{"Ki", 1 << 10},
	{"Mi", 1 << 20},
	{"Gi", 1 << 30},
	{"Ti", 1 << 40},
	{"k", 1e3},
	{"K", 1e3},
	{"M", 1e6},
	{"G", 1e9},
	{"T", 1e12},
}

// parseQuantityBytes converts a Kubernetes quantity to bytes ("123Mi", "1G",
// "1048576").
func parseQuantityBytes(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	for _, entry := range byteSuffixes {
		if strings.HasSuffix(raw, entry.suffix) {
			value, err := strconv.ParseFloat(strings.TrimSuffix(raw, entry.suffix), 64)
			if err != nil {
				return 0
			}
			return int64(value * entry.scale)
		}
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int64(value)
}
