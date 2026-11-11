package profanity

import "testing"

func TestReplace(t *testing.T) {
	f := New([]string{"damn", "Shit", "  heck  "})
	tests := []struct{ name, in, want string }{
		{"a banned word", "damn it", "*** it"},
		{"lower case match of an upper case word", "DAMN it", "*** it"},
		{"mixed case", "DaMn IT", "*** IT"},
		{"the list is lower-cased too", "shit happens", "*** happens"},
		{"the list is trimmed", "oh heck", "oh ***"},
		{"whole word only: inside a longer word", "damnation and shitake", "damnation and shitake"},
		{"whole word only: a longer word that contains it at the end", "goddamn", "goddamn"},
		{"punctuation around the word stays", "(damn!), damn.", "(***!), ***."},
		{"a hyphen ends the word", "damn-it", "***-it"},
		{"the same word twice", "damn damn", "*** ***"},
		{"at the very start and end", "damn", "***"},
		{"spaces and newlines are kept", "a  damn\n\tb", "a  ***\n\tb"},
		{"a word with digits is another word", "damn2 2damn", "damn2 2damn"},
		{"non-Latin text is not mangled", "привет мир 😀 damn", "привет мир 😀 ***"},
		{"upper case non-Latin word in the list", "ЗЛО тут", "*** тут"},
		{"nothing to replace", "good game", "good game"},
		{"empty", "", ""},
	}
	f2 := New([]string{"damn", "shit", "heck", "зло"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f2.Replace(tt.in); got != tt.want {
				t.Errorf("Replace(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	// The list given to New is normalised (case, spaces).
	if got := f.Replace("Damn SHIT heck"); got != "*** *** ***" {
		t.Errorf("Replace with an unnormalised list = %q", got)
	}
}

func TestNilAndEmptyFilterChangeNothing(t *testing.T) {
	var nilFilter *Filter
	for _, f := range []*Filter{nilFilter, New(nil)} {
		if got := f.Replace("damn it"); got != "damn it" {
			t.Errorf("got %q", got)
		}
	}
}

func TestDefaultListCatchesCommonWordsButNotInnocentOnes(t *testing.T) {
	f := Default()
	for _, w := range []string{"fuck", "SHIT", "Bitch", "asshole"} {
		if got := f.Replace("you " + w + "!"); got != "you ***!" {
			t.Errorf("%q was not replaced: %q", w, got)
		}
	}
	for _, ok := range []string{"class", "assassin", "Scunthorpe", "cocktail", "Dickens", "analysis", "passage", "bass guitar", "hello", "grape", "shell"} {
		if got := f.Replace(ok); got != ok {
			t.Errorf("%q was changed to %q", ok, got)
		}
	}
}
