package format

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// winReserved are DOS device names that Windows still refuses as filenames.
// A name is reserved on its base alone, so NUL.txt and NUL.tar.gz are both
// the null device. Windows 11 relaxed most of this, but older versions
// haven't, and applying it everywhere keeps a synced download folder portable.
var winReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SafeName strips anything a filesystem would object to. The Windows set is
// the strictest, so use it everywhere and keep filenames portable.
func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)
	// A release name can run past the 255-byte filename limit once the
	// extension is added. Cut by runes, then re-trim what the cut exposed.
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ".") // leading/trailing dots are stripped by Windows
	if s == "" {
		return "untitled"
	}

	base := s
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	if winReserved[strings.ToLower(base)] {
		s = "_" + s
	}
	return s
}

// ExtFromURL pulls a file extension off the URL, defaulting to .mkv.
func ExtFromURL(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	ext := strings.ToLower(filepath.Ext(raw))
	switch ext {
	case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".ts", ".webm", ".wmv", ".flv":
		return ext
	}
	return ".mkv"
}

var spaceRun = regexp.MustCompile(`\s+`)

// tidySegment cleans up after a placeholder resolved to nothing — an absent
// year would otherwise leave "() Inception" behind.
func tidySegment(s string) string {
	for _, empty := range []string{"()", "[]", "{}", "- -"} {
		s = strings.ReplaceAll(s, empty, "")
	}
	s = spaceRun.ReplaceAllString(s, " ")
	return strings.Trim(s, " -_·.")
}

func ExpandPattern(pattern string, vals map[string]string) []string {
	// Split first, fill in after: a title like "Fate/Zero" must stay one
	// folder name, not become two. One Replacer pass also means a value that
	// happens to contain "{year}" is not expanded again.
	pairs := make([]string, 0, len(vals)*2)
	for k, v := range vals {
		pairs = append(pairs, "{"+k+"}", v)
	}
	fill := strings.NewReplacer(pairs...)

	// Either separator: a Windows user will reach for a backslash, and
	// letting it fall through to SafeName would turn the folder break into a
	// literal "-" in the filename.
	var segs []string
	for _, seg := range strings.FieldsFunc(pattern, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if seg = tidySegment(fill.Replace(seg)); seg != "" {
			segs = append(segs, SafeName(seg))
		}
	}
	return segs
}

// Bytes uses decimal units, as the addons and the Stremio app do — in
// binary a 37.3 GB file reads as 34.7, and the two never agree.
func Bytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func BaseName(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
