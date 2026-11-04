package ui

import (
	"github.com/charmbracelet/lipgloss"
	"stremio-cliuwu/internal/format"
	"stremio-cliuwu/internal/streams"
	"stremio-cliuwu/internal/stremio"
	"strings"
)

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

func parseStream(s stremio.Stream) streamInfo {
	cleanName := streams.StripInvis(streams.StarRe.ReplaceAllString(s.Name, ""))
	cleanTitle := streams.StripInvis(s.Title)
	cleanDesc := streams.StripInvis(s.Description)
	filename := streams.StripInvis(s.BehaviorHints.Filename)

	in := streamInfo{
		Cached:  streams.Cached(s),
		Library: streams.InLibrary(s),
		Src:     streams.ExtractSrc(cleanName),
	}

	// The name first, aliases included — it's the addon's own verdict, and
	// AIOStreams writes "🚀 FHD" there. Then the release name. Aliases are
	// read in the name only: in a filename, "UHD.BluRay.1080p" is a 1080p
	// release whatever the UHD says.
	if in.Res = streams.ExtractRes(cleanName); in.Res == "" {
		in.Res = streams.ResAlias(cleanName)
	}
	for _, hay := range []string{cleanTitle, filename, cleanDesc} {
		if in.Res != "" {
			break
		}
		in.Res = streams.ExtractRes(hay)
	}

	// behaviorHints first: videoSize is part of the stream spec, so where an
	// addon sets it it's exact. Scraping free text is the fallback. Torrentio
	// puts its size in a multi-line title, "👤 50 💾 1.44 GB ⚙️ Provider".
	in.Bytes = s.BehaviorHints.VideoSize
	for _, hay := range []string{cleanDesc, cleanTitle, cleanName} {
		if in.Bytes > 0 {
			break
		}
		in.Bytes = streams.ParseSize(hay)
	}

	text := s.Description + "\n" + s.Title + "\n" + s.BehaviorHints.Filename
	in.HDR = streams.ExtractHDR(text)
	in.Dur = streams.ExtractDuration(cleanDesc + " " + cleanTitle)
	in.Codec, in.Langs = streams.ExtractAudio(s.Description+"\n"+s.Title, filename)
	in.Detail = streamDetailLines(s)

	in.Filename = filename
	if in.Filename == "" {
		in.Filename = cleanTitle
	}
	if in.Filename == "" {
		leftover := streams.ResRe.ReplaceAllString(cleanName, "")
		leftover = streams.SourceRe.ReplaceAllString(leftover, "")
		leftover = streams.SizeRe.ReplaceAllString(leftover, "")
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
		size = format.Bytes(in.Bytes)
	}

	// The runtime is quiet unless it says something: a copy more than a
	// minute off the rest is likely a different cut or a mislabelled file,
	// and one shorter than where you stopped can't resume you there.
	dur := ""
	if in.Dur > 0 {
		dur = grey(format.Secs(float64(in.Dur)))
		switch {
		case o.Resume > 5 && float64(in.Dur) < o.Resume:
			dur = stErr.Render(format.Secs(float64(in.Dur)))
		case o.Median > 0 && abs(in.Dur-o.Median) > 60:
			dur = yell(format.Secs(float64(in.Dur)))
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
func streamDetailLines(s stremio.Stream) []string {
	clean := func(text string) []string {
		var out []string
		for _, ln := range strings.Split(text, "\n") {
			if ln = streams.StripInvis(ln); ln != "" {
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

// fmtAudio fits the audio column: format, then the first two languages and
// a + when there are more.
func fmtAudio(codec string, langs []string) string {
	out := grey(codec)
	if len(langs) > 0 {
		l := strings.Join(langs[:min(2, len(langs))], "/")
		if len(langs) > 2 {
			l += "+"
		}
		if out != "" {
			out = padRight(out, 9) + " "
		}
		out += l
	}
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
