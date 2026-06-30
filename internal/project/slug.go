package project

import (
	"regexp"
	"strings"
)

var fillerWords = map[string]bool{
	"a": true, "an": true, "the": true, "for": true,
	"to": true, "in": true, "on": true, "of": true,
	"and": true, "with": true, "from": true, "that": true,
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

// DeriveSlug produces a kebab-case slug from a freeform task description.
// Strips filler words, collapses non-alphanumeric runs to hyphens, caps at
// 40 characters with trailing hyphens trimmed.
func DeriveSlug(task string) string {
	lower := strings.ToLower(task)
	words := strings.Fields(lower)
	var filtered []string
	for _, w := range words {
		clean := nonAlphaNum.ReplaceAllString(w, "")
		if clean != "" && !fillerWords[clean] {
			filtered = append(filtered, clean)
		}
	}
	slug := strings.Join(filtered, "-")
	slug = nonAlphaNum.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = slug[:40]
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}
