package programview

import (
	"strings"
	"unicode"
)

const (
	displayTitleLimit = 72
	summaryLimit      = 260
)

// displayIdentity derives a short human title and one-line summary for a
// program. The title prefers the first H1 in goal.md; without one it falls
// back to a shortened program title so the UI never renders a raw prompt as
// its heading.
func displayIdentity(title, goal string) (string, string) {
	heading, paragraph := goalHeadingAndParagraph(goal)
	displayTitle := heading
	if displayTitle == "" {
		displayTitle = shorten(firstSentence(title), displayTitleLimit)
	}
	return displayTitle, shorten(paragraph, summaryLimit)
}

// goalHeadingAndParagraph reads the first H1 and the first prose paragraph
// that follows it. Lists, quotes, code fences, raw HTML and further headings
// are skipped so the summary is always a sentence a person wrote.
func goalHeadingAndParagraph(goal string) (string, string) {
	lines := prose(goal)
	heading := ""
	start := 0
	for index, line := range lines {
		if isATXHeading(line) {
			heading = cleanInline(strings.TrimLeft(line, "# "))
			start = index + 1
			break
		}
	}
	buffer := []string{}
	for _, line := range lines[start:] {
		if line == "" || isATXHeading(line) || isSetextUnderline(line) {
			if len(buffer) > 0 {
				break
			}
			continue
		}
		if !isProseParagraphLine(line) {
			if len(buffer) > 0 {
				break
			}
			continue
		}
		buffer = append(buffer, cleanInline(line))
	}
	return heading, strings.Join(buffer, " ")
}

// prose trims a markdown document down to the lines that can carry meaning:
// fenced code and raw HTML are dropped entirely.
func prose(goal string) []string {
	lines := strings.Split(strings.ReplaceAll(goal, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	fenced := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || strings.HasPrefix(line, "<") {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

func isATXHeading(line string) bool {
	return strings.HasPrefix(line, "#") && strings.Contains(line, " ")
}

func isSetextUnderline(line string) bool {
	return strings.Trim(line, "=") == "" || strings.Trim(line, "-") == ""
}

// isProseParagraphLine rejects list items, block quotes, tables and horizontal
// rules so a bulleted goal file does not become the program summary.
func isProseParagraphLine(line string) bool {
	switch {
	case strings.HasPrefix(line, ">"), strings.HasPrefix(line, "|"),
		strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "),
		strings.HasPrefix(line, "+ "):
		return false
	}
	for index, char := range line {
		if unicode.IsDigit(char) {
			continue
		}
		if (char == '.' || char == ')') && index > 0 && index <= 2 {
			return false
		}
		break
	}
	return true
}

// cleanInline removes the markdown emphasis and code punctuation that would
// otherwise leak into a plain-text heading.
func cleanInline(value string) string {
	replacer := strings.NewReplacer("`", "", "**", "", "__", "", "*", "", "_", "")
	cleaned := replacer.Replace(strings.TrimSpace(strings.TrimRight(value, "#")))
	return strings.Join(strings.Fields(cleaned), " ")
}

// firstSentence keeps the opening sentence of a long prompt-shaped title.
func firstSentence(value string) string {
	cleaned := cleanInline(value)
	for index, char := range cleaned {
		if char != '.' && char != '!' && char != '?' {
			continue
		}
		if index+1 >= len(cleaned) || cleaned[index+1] == ' ' {
			return strings.TrimSpace(cleaned[:index])
		}
	}
	return cleaned
}

// shorten clamps at a word boundary so the UI never has to hide overflow.
func shorten(value string, limit int) string {
	cleaned := strings.Join(strings.Fields(value), " ")
	if len([]rune(cleaned)) <= limit {
		return cleaned
	}
	runes := []rune(cleaned)[:limit]
	if cut := strings.LastIndex(string(runes), " "); cut > limit/2 {
		runes = []rune(string(runes)[:cut])
	}
	return strings.TrimRight(strings.TrimSpace(string(runes)), ",;:-") + "…"
}
