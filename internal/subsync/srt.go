// Package subsync retimes a subtitle against a better-timed one, without
// touching the video. alass does the aligning; this package gets two subtitle
// files into a shape alass accepts and hands the result back.
package subsync

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// tagRe matches one inline markup tag, opening or closing.
var tagRe = regexp.MustCompile(`</?([^>\s/]*)[^>]*>`)

// ToSRT converts WebVTT to SubRip. Addons often serve WebVTT under a .srt
// name, and alass picks its parser from the extension, so the content is what
// has to be checked. Anything that does not start with WEBVTT (after an
// optional BOM) is returned untouched, SubRip and ASS included.
func ToSRT(data []byte) []byte {
	body := bytes.TrimPrefix(data, utf8BOM)
	if !bytes.HasPrefix(body, []byte("WEBVTT")) {
		return data
	}

	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var out strings.Builder
	n := 0
	for i, block := range splitBlocks(text) {
		if i == 0 {
			continue // the WEBVTT header, with any title and metadata lines
		}
		start, end, lines, ok := parseCue(block)
		if !ok {
			continue // NOTE, STYLE, REGION or a block with no timing line
		}
		n++
		fmt.Fprintf(&out, "%d\n%s --> %s\n", n, start, end)
		for _, l := range lines {
			out.WriteString(stripTags(l))
			out.WriteByte('\n')
		}
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

// splitBlocks groups lines into blocks separated by one or more blank lines.
func splitBlocks(text string) [][]string {
	var blocks [][]string
	var cur []string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

// parseCue finds the timing line (the first or second line of a cue block —
// the first can be an identifier) and returns normalised timestamps and the
// text lines after it.
func parseCue(block []string) (start, end string, text []string, ok bool) {
	for i := 0; i < len(block) && i < 2; i++ {
		if !strings.Contains(block[i], "-->") {
			continue
		}
		f := strings.Fields(block[i])
		if len(f) < 3 || f[1] != "-->" {
			return "", "", nil, false
		}
		start, end = srtTime(f[0]), srtTime(f[2])
		if start == "" || end == "" {
			return "", "", nil, false
		}
		return start, end, block[i+1:], true
	}
	return "", "", nil, false
}

// srtTime turns [HH:]MM:SS.mmm into HH:MM:SS,mmm, or "" if it isn't one.
func srtTime(s string) string {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return ""
	}
	h := "00"
	if len(parts) == 3 {
		h, parts = parts[0], parts[1:]
	}
	sec, ms, _ := strings.Cut(strings.ReplaceAll(parts[1], ",", "."), ".")
	for len(ms) < 3 {
		ms += "0"
	}
	if len(h) != 2 || len(parts[0]) != 2 || len(sec) != 2 || len(ms) != 3 {
		return ""
	}
	return fmt.Sprintf("%s:%s:%s,%s", h, parts[0], sec, ms)
}

// stripTags removes WebVTT markup. SubRip players understand <i>, <b> and
// <u>, so those stay; <c.class>, <v Name> and the rest go.
func stripTags(line string) string {
	return tagRe.ReplaceAllStringFunc(line, func(tag string) string {
		switch strings.ToLower(tagRe.FindStringSubmatch(tag)[1]) {
		case "i", "b", "u":
			return tag
		}
		return ""
	})
}
