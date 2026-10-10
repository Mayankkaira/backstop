package pgstat

import "strings"

var hotPrefixes = []string{"select", "insert", "update", "delete", "with", "copy", "merge"}

func IsHotQuery(q string) bool {
	s := strings.ToLower(strings.TrimSpace(q))
	for _, p := range hotPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func CompactQuery(q string, max int) string {
	s := strings.Join(strings.Fields(q), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
