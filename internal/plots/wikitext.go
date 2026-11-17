package plots

import (
	"html"
	"regexp"
	"strings"
)

// MinPlotWords is the shortest plot worth embedding. Shorter sections are
// usually a stub or a "see the list of episodes" pointer.
const MinPlotWords = 40

// MaxInputWords caps the plot text handed to the model. bge-small reads 512
// tokens, about 380 words of prose, so the rest would be cut anyway.
const MaxInputWords = 400

// plotHeadings are the level-2 section names that hold a plot or a premise.
var plotHeadings = map[string]bool{
	"plot": true, "plot summary": true, "synopsis": true, "premise": true,
	"summary": true, "story": true, "storyline": true, "overview": true,
}

var (
	reComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	reRefShort = regexp.MustCompile(`(?is)<ref[^>]*/>`)
	reRef      = regexp.MustCompile(`(?is)<ref[^>]*>.*?</ref>`)
	reTemplate = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	reTable    = regexp.MustCompile(`(?s)\{\|.*?\|\}`)
	reLink     = regexp.MustCompile(`\[\[(?:[^|\]]*\|)?([^\]]*)\]\]`)
	reExtLink  = regexp.MustCompile(`\[(?:https?:)?//[^\s\]]+\s*([^\]]*)\]`)
	reQuotes   = regexp.MustCompile(`'{2,}`)
	reTag      = regexp.MustCompile(`<[^>]+>`)
	reHeading  = regexp.MustCompile(`(?m)^\s*=+[^=\n].*?=+\s*$`)
)

// ExtractPlot returns the cleaned text of the article's plot section and its length
// in words. Both are empty when the article has no plot section.
func ExtractPlot(wikitext string) (string, int) {
	body := plotSection(reComment.ReplaceAllString(wikitext, ""))
	if body == "" {
		return "", 0
	}
	text := clean(body)
	return text, len(strings.Fields(text))
}

// EmbedInput is the text the model sees for a plot: its first MaxInputWords
// words. It is built from Wikipedia text only.
func EmbedInput(plot string) string {
	words := strings.Fields(plot)
	return strings.Join(words[:min(len(words), MaxInputWords)], " ")
}

// plotSection returns the body of the first level-2 section with a plot
// heading, subsections included, up to the next level-2 heading.
func plotSection(wikitext string) string {
	var body []string
	in := false
	for line := range strings.SplitSeq(wikitext, "\n") {
		level, name := heading(line)
		if level > 0 && level <= 2 {
			if in {
				break
			}
			in = level == 2 && plotHeadings[strings.ToLower(name)]
			continue
		}
		if in {
			body = append(body, line)
		}
	}
	return strings.Join(body, "\n")
}

// heading reports a line's heading level and name, or level 0 for a line that
// is not a heading. The level is the smaller of the two runs of "=".
func heading(line string) (int, string) {
	s := strings.TrimSpace(line)
	if len(s) < 3 || s[0] != '=' || s[len(s)-1] != '=' {
		return 0, ""
	}
	open := len(s) - len(strings.TrimLeft(s, "="))
	closing := len(s) - len(strings.TrimRight(s, "="))
	if open+closing >= len(s) {
		return 0, ""
	}
	level := min(open, closing)
	return level, strings.TrimSpace(s[level : len(s)-level])
}

// clean turns wikitext into plain prose: references, templates, tables,
// files, markup and subsection headings are dropped, and links keep their
// visible text.
func clean(wt string) string {
	wt = reComment.ReplaceAllString(wt, "")
	wt = reRefShort.ReplaceAllString(wt, "")
	wt = reRef.ReplaceAllString(wt, "")
	for range 10 { // templates nest, so strip the innermost until none are left
		next := reTemplate.ReplaceAllString(wt, "")
		if next == wt {
			break
		}
		wt = next
	}
	wt = reTable.ReplaceAllString(wt, "")
	wt = dropFiles(wt)
	wt = reHeading.ReplaceAllString(wt, "")
	wt = reLink.ReplaceAllString(wt, "$1")
	wt = reExtLink.ReplaceAllString(wt, "$1")
	wt = reQuotes.ReplaceAllString(wt, "")
	wt = reTag.ReplaceAllString(wt, "")
	wt = html.UnescapeString(wt)
	return strings.Join(strings.Fields(wt), " ") // Fields also splits on no-break spaces
}

// dropFiles removes file, image and category links, whose captions can hold
// links of their own, by matching the brackets.
func dropFiles(wt string) string {
	var b strings.Builder
	for {
		start := fileLinkStart(wt)
		if start < 0 {
			b.WriteString(wt)
			return b.String()
		}
		b.WriteString(wt[:start])
		depth, end := 0, len(wt)
		for i := start; i < len(wt)-1; i++ {
			switch wt[i : i+2] {
			case "[[":
				depth++
				i++
			case "]]":
				depth--
				i++
			}
			if depth == 0 {
				end = i + 1
				break
			}
		}
		wt = wt[end:]
	}
}

func fileLinkStart(wt string) int {
	lower := strings.ToLower(wt)
	best := -1
	for _, prefix := range []string{"[[file:", "[[image:", "[[category:"} {
		if i := strings.Index(lower, prefix); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}
