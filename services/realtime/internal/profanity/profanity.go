// Package profanity replaces banned words in a chat message with ***.
//
// A word is a run of letters and digits. It is replaced only when the whole word, in lower case, is
// in the list: "class" is not touched because it contains "ass". Spelling tricks (f*ck, f u c k,
// leet speak) are not caught; this is a first line of defence, not a moderation system.
package profanity

import (
	_ "embed"
	"strings"
	"unicode"
)

//go:embed words.txt
var defaultWords string

// Mask is what a banned word is replaced with, whatever its length.
const Mask = "***"

// Filter replaces the words of its list. It is safe for concurrent use (it never changes after New).
type Filter struct {
	words map[string]struct{}
}

// New returns a Filter for the given words (compared in lower case).
func New(words []string) *Filter {
	f := &Filter{words: make(map[string]struct{}, len(words))}
	for _, w := range words {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			f.words[w] = struct{}{}
		}
	}
	return f
}

// Default returns a Filter with the list in words.txt.
func Default() *Filter {
	var words []string
	for _, line := range strings.Split(defaultWords, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			words = append(words, line)
		}
	}
	return New(words)
}

// Replace returns text with every banned word replaced by Mask. Everything else, spaces and
// punctuation included, stays exactly as it was. A nil Filter changes nothing.
func (f *Filter) Replace(text string) string {
	if f == nil || len(f.words) == 0 {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	start := -1 // byte index where the current word started, -1 outside a word
	flush := func(end int) {
		word := text[start:end]
		if _, banned := f.words[strings.ToLower(word)]; banned {
			out.WriteString(Mask)
		} else {
			out.WriteString(word)
		}
		start = -1
	}
	for i, r := range text {
		if isWordRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			flush(i)
		}
		out.WriteRune(r)
	}
	if start >= 0 {
		flush(len(text))
	}
	return out.String()
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}
