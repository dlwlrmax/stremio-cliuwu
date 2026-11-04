// Package langs normalises language codes and names. Subtitle and audio
// sources disagree on how to spell a language ("eng", "en", "English"); this
// maps them all to one canonical display name.
package langs

import (
	"fmt"
	"sort"
	"strings"
)

// Preferred splits the subtitle language setting into canonical names.
//
// It's a list, not a single value — mpv's --slang takes a comma-separated
// preference order, so "eng, en, English" is a reasonable thing to type. It
// was being treated as one language name, which of course matched nothing.
func Preferred(setting string) []string {
	var out []string
	seen := map[string]bool{}

	for _, part := range strings.Split(setting, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n := Name(part); !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// Rank is a language's position in the preference list, or a large number
// if it isn't in it at all.
func Rank(prefs []string, lang string) int {
	for i, p := range prefs {
		if p == lang {
			return i
		}
	}
	return len(prefs) + 1
}

// codes maps every code we recognise to a canonical display name.
// Package level so the settings screen can present it in reverse: which
// codes count as which language.
var codes = map[string]string{
	"eng": "English", "en": "English",
	"spa": "Spanish", "es": "Spanish",
	"fre": "French", "fra": "French", "fr": "French",
	"ger": "German", "deu": "German", "de": "German",
	"ita": "Italian", "it": "Italian",
	"por": "Portuguese", "pt": "Portuguese",
	"rus": "Russian", "ru": "Russian",
	"jpn": "Japanese", "ja": "Japanese",
	"kor": "Korean", "ko": "Korean",
	"chi": "Chinese", "zho": "Chinese", "zh": "Chinese",
	"ara": "Arabic", "ar": "Arabic",
	"dut": "Dutch", "nld": "Dutch", "nl": "Dutch",
	"pol": "Polish", "pl": "Polish",
	"tur": "Turkish", "tr": "Turkish",
	"swe": "Swedish", "sv": "Swedish",
	"dan": "Danish", "da": "Danish",
	"fin": "Finnish", "fi": "Finnish",
	"nor": "Norwegian", "no": "Norwegian",
	"heb": "Hebrew", "he": "Hebrew",
	"hin": "Hindi", "hi": "Hindi",
	"ell": "Greek", "gre": "Greek", "el": "Greek",
	"ces": "Czech", "cze": "Czech", "cs": "Czech",
	"ron": "Romanian", "rum": "Romanian", "ro": "Romanian",
	"hun": "Hungarian", "hu": "Hungarian",
	"tha": "Thai", "th": "Thai",
	"vie": "Vietnamese", "vi": "Vietnamese",
	"ind": "Indonesian", "id": "Indonesian",
	"ukr": "Ukrainian", "uk": "Ukrainian",
}

// Name is the canonical display name for a language, and doubles as the
// grouping key.
//
// Addons are inconsistent: the same language arrives as "eng", "en" and
// "English" from three sources, which made three separate tabs for one
// language. Normalising on the way in collapses them. The spec explicitly
// allows free text here, so anything unrecognised passes through as itself
// rather than being dropped.
func Name(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return "unknown"
	}
	if n, ok := codes[code]; ok {
		return n
	}

	// Regional variants: en-GB, pt_BR, es-419. The region only narrows a
	// language it's already named, so the base code decides.
	if i := strings.IndexAny(code, "-_"); i > 0 {
		if n, ok := codes[code[:i]]; ok {
			return n
		}
	}

	// Already a name rather than a code: "english", "brazilian portuguese".
	for _, n := range codes {
		if strings.EqualFold(n, code) {
			return n
		}
	}
	return code
}

// Reference lists each language with the codes that resolve to it, for
// the settings prompt — otherwise there's no way to know what to type.
func Reference() []string {
	byName := map[string][]string{}
	for code, name := range codes {
		byName[name] = append(byName[name], code)
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}

	out := make([]string, 0, len(names))
	for _, n := range names {
		codes := byName[n]
		sort.Slice(codes, func(i, j int) bool {
			if len(codes[i]) != len(codes[j]) {
				return len(codes[i]) > len(codes[j]) // three-letter first
			}
			return codes[i] < codes[j]
		})
		out = append(out, fmt.Sprintf("%-*s  %s", width, n, strings.Join(codes, " · ")))
	}
	return out
}
