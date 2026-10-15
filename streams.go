package main

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

var (
	invisRe  = regexp.MustCompile(`[\x{200b}-\x{200f}\x{2060}-\x{206f}\x{fe0f}\x{00ad}\x{feff}\x{200d}\x{200c}\x{180e}\x{00a0}\x{202a}-\x{202e}\x{2028}\x{2029}]+`)
	resRe    = regexp.MustCompile(`(?i)\b(4K|2K|2160[Pp]|1440[Pp]|1080[Pp]|720[Pp]|480[Pp]|360[Pp])\b`)
	sourceRe = regexp.MustCompile(`[〈<]([^〉>]+)[〉>]`)
	sizeRe   = regexp.MustCompile(`(?i)(\d+\.?\d*)\s*(GB|MB|GiB|MiB)`)
	starRe   = regexp.MustCompile(`[★☆✦✧⭐]+`)
)

func stripInvis(s string) string {
	s = invisRe.ReplaceAllString(s, " ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func extractRes(s string) string {
	m := resRe.FindString(s)
	if m == "" {
		return ""
	}
	switch strings.ToUpper(m) {
	case "2160P":
		return "4K"
	case "1440P":
		return "2K"
	default:
		return strings.ToUpper(m)
	}
}

func extractSrc(s string) string {
	m := sourceRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// streamInfo is everything a stream row shows, read once from whatever the
// addon put in its name, title, description and behaviorHints.
type streamInfo struct {
	Res      string // "4K", "1080P", …
	Cached   int    // as streamCached
	Library  bool
	HDR      string // "DV", "HDR+", "HDR" or ""
	Bytes    int64  // 0 when unknown
	Dur      int    // runtime in seconds, 0 when unknown
	Codec    string // first audio track's format
	Langs    []string
	Src      string
	Filename string
	Detail   []string // the details pane's lines
}

func parseStream(s Stream) streamInfo {
	cleanName := stripInvis(starRe.ReplaceAllString(s.Name, ""))
	cleanTitle := stripInvis(s.Title)
	cleanDesc := stripInvis(s.Description)
	filename := stripInvis(s.BehaviorHints.Filename)

	in := streamInfo{
		Cached:  streamCached(s),
		Library: streamInLibrary(s),
		Src:     extractSrc(cleanName),
	}

	// The name first, aliases included — it's the addon's own verdict, and
	// AIOStreams writes "🚀 FHD" there. Then the release name. Aliases are
	// read in the name only: in a filename, "UHD.BluRay.1080p" is a 1080p
	// release whatever the UHD says.
	if in.Res = extractRes(cleanName); in.Res == "" {
		in.Res = resAlias(cleanName)
	}
	for _, hay := range []string{cleanTitle, filename, cleanDesc} {
		if in.Res != "" {
			break
		}
		in.Res = extractRes(hay)
	}

	// behaviorHints first: videoSize is part of the stream spec, so where an
	// addon sets it it's exact. Scraping free text is the fallback. Torrentio
	// puts its size in a multi-line title, "👤 50 💾 1.44 GB ⚙️ Provider".
	in.Bytes = s.BehaviorHints.VideoSize
	for _, hay := range []string{cleanDesc, cleanTitle, cleanName} {
		if in.Bytes > 0 {
			break
		}
		in.Bytes = parseSize(hay)
	}

	text := s.Description + "\n" + s.Title + "\n" + s.BehaviorHints.Filename
	in.HDR = extractHDR(text)
	in.Dur = extractDuration(cleanDesc + " " + cleanTitle)
	in.Codec, in.Langs = extractAudio(s.Description+"\n"+s.Title, filename)
	in.Detail = streamDetailLines(s)

	in.Filename = filename
	if in.Filename == "" {
		in.Filename = cleanTitle
	}
	if in.Filename == "" {
		leftover := resRe.ReplaceAllString(cleanName, "")
		leftover = sourceRe.ReplaceAllString(leftover, "")
		leftover = sizeRe.ReplaceAllString(leftover, "")
		leftover = strings.ReplaceAll(leftover, "⚡", "")
		leftover = strings.ReplaceAll(leftover, "⏳", "")
		in.Filename = strings.Join(strings.Fields(leftover), " ")
	}
	return in
}

// rowOpts is the list-wide context a row is drawn in.
type rowOpts struct {
	Width  int
	Audio  bool    // show the audio column
	Median int     // typical runtime across the results, 0 if unknown
	Resume float64 // where you stopped, 0 if nowhere
}

// streamCol is one fixed-width column. Shared by the rows and the header so
// the two can't drift out of line.
type streamCol struct {
	head  string
	width int
	right bool
}

func streamCols(o rowOpts) []streamCol {
	cols := []streamCol{
		{"res", 5, false}, // "1080P" is the widest
		{"", 2, false},    // ⚡ / ⏳
		{"", 2, false},    // 📌
		{"hdr", 4, false},
		{"size", 8, true}, // "116.6 GB"
		{"time", 7, true}, // "2:13:22"
	}
	if o.Audio {
		cols = append(cols, streamCol{"audio", 16, false}) // "DTS-HD MA EN/JA+"
	}
	return cols
}

func (c streamCol) pad(s string) string {
	if c.right {
		return padLeft(s, c.width)
	}
	return padRight(s, c.width)
}

// StreamHeader labels the columns, aligned with FmtStream's rows. The column
// the list is sorted by gets an arrow, which says more than a footer entry.
func StreamHeader(o rowOpts, sortBy string, reverse bool) string {
	arrow := "↓"
	if reverse {
		arrow = "↑"
	}
	var parts []string
	for _, c := range streamCols(o) {
		h := c.head
		if (sortBy == "size" && h == "size") || (sortBy == "res" && h == "res") {
			h = arrow + h
		}
		parts = append(parts, c.pad(h))
	}
	parts = append(parts, "file")
	// 8: the list's own prefix — cursor, number, tick.
	return strings.Repeat(" ", 8) + grey(strings.Join(parts, "  "))
}

// FmtStream produces a readable label for a stream entry.
func FmtStream(in streamInfo, o rowOpts) string {
	width := o.Width
	if width < 40 {
		width = 80 // not sized yet, or a very narrow terminal
	}

	cached := ""
	switch in.Cached {
	case 1:
		cached = "⚡"
	case -1:
		cached = "⏳"
	}
	library := ""
	if in.Library {
		library = "📌"
	}
	size := ""
	if in.Bytes > 0 {
		size = fmtBytes(in.Bytes)
	}

	// The runtime is quiet unless it says something: a copy more than a
	// minute off the rest is likely a different cut or a mislabelled file,
	// and one shorter than where you stopped can't resume you there.
	dur := ""
	if in.Dur > 0 {
		dur = grey(fmtSecs(float64(in.Dur)))
		switch {
		case o.Resume > 5 && float64(in.Dur) < o.Resume:
			dur = stErr.Render(fmtSecs(float64(in.Dur)))
		case o.Median > 0 && abs(in.Dur-o.Median) > 60:
			dur = yell(fmtSecs(float64(in.Dur)))
		}
	}

	hdr := in.HDR
	if hdr != "" {
		hdr = hi(hdr)
	}

	cells := []string{bold(in.Res), cached, library, hdr, grey(size), dur}
	if o.Audio {
		cells = append(cells, fmtAudio(in.Codec, in.Langs))
	}

	var parts []string
	for i, c := range streamCols(o) {
		parts = append(parts, c.pad(cells[i]))
	}
	if in.Src != "" {
		parts = append(parts, hi(in.Src))
	}

	if in.Filename == "" && in.Res == "" && cached == "" && size == "" {
		return grey("(no info)")
	}

	filename := in.Filename
	if filename != "" {
		// Whatever the row has left after the list's own prefix (cursor,
		// number, tick: 8) and the columns before it.
		used := 8 + lipgloss.Width(strings.Join(parts, "  ")) + 2
		maxFilename := max(20, width-used-1) // the ellipsis
		runes := []rune(filename)
		if len(runes) > maxFilename {
			filename = string(runes[:maxFilename]) + "…"
		}
		parts = append(parts, bold(filename))
	}
	// No addon tag: the provider bar already says where results came from,
	// and with one addon it was the same text on every row.
	return strings.Join(parts, "  ")
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// padRight and padLeft pad to a display width, which for emoji and styled
// text isn't the byte or rune count. Anything wider is clipped, so one odd
// value can't push the rest of its row out of line.
func padRight(s string, w int) string {
	if lipgloss.Width(s) > w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func padLeft(s string, w int) string {
	if lipgloss.Width(s) > w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s
}

// cachedMarkers are how the common debrid addons signal instant availability.
// Torrentio uses ⚡ plus a "[RD+]"-style provider tag; ⏳ means it would have to
// be downloaded first.
// Torrentio marks a debrid result "[TB+]" when it's ready to stream and
// "[TB download]" when it isn't, per provider. Both are matched precisely
// rather than by searching for the word "download" anywhere in the row — a
// release named ...WEB-DL.Download.Edition would otherwise be sorted to the
// bottom and shown with an hourglass it hasn't earned.
const debridProviders = `RD|TB|AD|PM|DL|OC|PR|EC`

var (
	cachedProviderRe  = regexp.MustCompile(`\[(` + debridProviders + `)\+\]`)
	pendingProviderRe = regexp.MustCompile(`(?i)\[(` + debridProviders + `)\s+download\]`)
)

// libraryRe matches AIOStreams' marker for a file already sitting in your
// debrid account — "📌 Library", with "🔍Library" as the indexer it came
// from. Anchored to the emoji so a release with "Library" in its name
// doesn't qualify.
var libraryRe = regexp.MustCompile(`(?i)[📌🔍]\s*library\b`)

// streamInLibrary reports whether the stream is already in your debrid
// library: no fetch, no waiting, and a copy you've chosen before.
func streamInLibrary(s Stream) bool {
	return libraryRe.MatchString(s.Name + " " + s.Description + " " + s.Title)
}

// streamCached classifies a stream: 1 cached, -1 explicitly not cached,
// 0 unknown (a direct HTTP addon, say, where the question doesn't apply).
func streamCached(s Stream) int {
	// An addon that states it wins over anything parsed out of the text.
	if s.Cached != nil {
		if *s.Cached {
			return 1
		}
		return -1
	}

	hay := s.Name + " " + s.Title + " " + s.Description

	switch {
	case strings.Contains(hay, "⚡"), cachedProviderRe.MatchString(hay):
		return 1
	case strings.Contains(hay, "⏳"), pendingProviderRe.MatchString(hay):
		return -1
	}
	return 0
}

// SortStreams orders the stream list. Cached-first matters more than
// resolution in practice: picking an uncached debrid result means waiting for
// the provider to fetch the torrent before mpv gets anything at all.
// matchWords flattens a release name so terms can be matched against it.
//
// Release naming has no agreed separator — the same tag turns up as "AI
// Upscale", "AI.Upscale" and "Ai_Upscaled" depending on who packed it. Every
// separator becomes a space, so one term covers all of them, and the result
// is padded so callers can anchor to word boundaries.
func matchWords(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '.', '_', '-', '[', ']', '(', ')', '{', '}', ',', '+', '/', ':':
			return ' '
		}
		return r
	}, s)
	return " " + strings.Join(strings.Fields(s), " ") + " "
}

// StreamBlocked reports whether a stream matches any of your blocked terms.
//
// Whole words, not substrings. Blocking "cam" should hide a camrip without
// also hiding Camelot, a film called Cam, or anything from a group with
// "cam" in its name — a block list you can't trust is worse than none. A
// trailing * makes a term match the start of a word instead, so "upscale*"
// covers upscaled and upscaler without listing each.
//
// Matched across the name, title, description and filename together: a
// release is sometimes flagged in the addon's label and sometimes only in
// the filename.
func StreamBlocked(s Stream, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	hay := matchWords(s.Name + " " + s.Title + " " + s.Description + " " +
		s.BehaviorHints.Filename)

	for _, raw := range terms {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}

		prefix := strings.HasSuffix(t, "*")
		t = matchWords(strings.TrimSuffix(t, "*"))
		if t = strings.TrimSuffix(t, " "); t == "" || t == " " {
			continue
		}

		if prefix {
			if strings.Contains(hay, t) { // t still carries its leading space
				return true
			}
			continue
		}
		if strings.Contains(hay, t+" ") {
			return true
		}
	}
	return false
}

// SortStreams orders by addon priority, then binge group, then library,
// then cached, then quality.
//
// binge is the bingeGroup of the release you last played for this show, ""
// for none. Matching it first is how Stremio keeps a season on one release:
// same group, same encode, same audio, and subtitles timed for it.
//
// Library comes before cached and quality: a file already in your debrid
// account is one you picked deliberately, and otherwise a preferred-quality
// match from the wider search would float above it.
//
// Addon order has to come first. With cached-first as the primary key, an
// addon serving direct debrid files — no ⚡ or [RD+] in the title, so it
// scores as unknown — sank below one whose results are detectably cached,
// however high you'd placed it. Reordering the list then looked like it did
// nothing at all for that addon.
func SortStreams(streams []Stream, preferred, binge string, cachedFirst bool) []Stream {
	pref := strings.ToUpper(preferred)

	// Keys are worked out once per stream: the library and cache checks are
	// regex matches, and a comparator runs them O(n log n) times over.
	type keyed struct {
		s       Stream
		binge   bool
		library bool
		cache   int
		quality bool
	}
	ks := make([]keyed, len(streams))
	for i, s := range streams {
		k := keyed{s: s, library: streamInLibrary(s)}
		k.binge = binge != "" && s.BehaviorHints.BingeGroup == binge
		if cachedFirst {
			k.cache = streamCached(s)
		}
		k.quality = pref != "" && strings.Contains(strings.ToUpper(s.Name+s.Title), pref)
		ks[i] = k
	}

	sort.SliceStable(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		switch {
		case a.s.Rank != b.s.Rank:
			return a.s.Rank < b.s.Rank
		case a.binge != b.binge:
			return a.binge
		case a.library != b.library:
			return a.library
		case a.cache != b.cache:
			return a.cache > b.cache
		}
		return a.quality && !b.quality
	})
	for i, k := range ks {
		streams[i] = k.s
	}
	return streams
}

// streamDetailMax caps the details pane. AIOStreams' default format runs to
// nine or ten lines with the name, and 8 was cutting its cache and library
// line; past this, a long description starts eating the list it describes.
// layout still drops the pane entirely on a terminal too short for it.
const streamDetailMax = 12

// streamDetailLines is the stream's own text, line for line, the way the
// Stremio app shows it: the addon's name block, then its description.
//
// Cleaned per line rather than as a whole — stripInvis folds whitespace, and
// the line breaks are the layout the addon chose.
func streamDetailLines(s Stream) []string {
	clean := func(text string) []string {
		var out []string
		for _, ln := range strings.Split(text, "\n") {
			if ln = stripInvis(ln); ln != "" {
				out = append(out, ln)
			}
		}
		return out
	}

	var lines []string
	if name := clean(s.Name); len(name) > 0 {
		lines = append(lines, bold(strings.Join(name, " · ")))
	}
	// Description is the spec's field; title is the older one many addons
	// still fill instead. Either, not both — some set them to the same text.
	body := s.Description
	if strings.TrimSpace(body) == "" {
		body = s.Title
	}
	lines = append(lines, clean(body)...)

	if len(lines) > streamDetailMax {
		lines = append(lines[:streamDetailMax-1], grey("…"))
	}
	return lines
}
