package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Parsing for the stream row's columns: the details an addon spells out in
// free text rather than in any field the spec defines.

var (
	// Only when no number is given anywhere. AIOStreams labels a 1080p
	// result "🚀 FHD" and a 4K one "🔥4K UHD".
	resAliasRe = regexp.MustCompile(`(?i)\b(FHD|UHD|QHD)\b`)

	// DV before HDR: a Dolby Vision release nearly always carries an HDR10
	// base layer too, and DV is the one worth knowing about.
	dvRe      = regexp.MustCompile(`(?i)\b(DV|DoVi|Dolby[ ._-]?Vision)\b`)
	hdrPlusRe = regexp.MustCompile(`(?i)\bHDR10(\+|[ ._-]?Plus)`)
	hdrRe     = regexp.MustCompile(`(?i)\b(HDR10|HDR|HLG)\b`)

	// AIOStreams' runtime, "⏱️ 2h:13m:22s". The emoji anchors it so a "1h"
	// in a filename isn't taken for one.
	durationRe = regexp.MustCompile(`⏱\S*\s*(?:(\d+)h)?:?\s*(?:(\d+)m)?:?\s*(?:(\d+)s)?`)

	// AIOStreams' audio line: "🎧 TrueHD | DTS-HD MA 🔊 5.1 🗣️ 🇬🇧 / 🇯🇵📝 🇬🇧".
	// Formats after the headphones, spoken languages after the speaking
	// head, up to the subtitle marker.
	codecLineRe = regexp.MustCompile(`🎧\S*\s*([^|🔊🗣\n]+)`)
	langLineRe  = regexp.MustCompile(`🗣\S*\s*([^📝\n]*)`)

	// For addons that don't spell it out, the release name usually does.
	codecNameRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(TrueHD|DTS-HD[ ._-]?MA|DTS-HD|DTS-X|DTS|E-?AC-?3|DDP|DD\+|AC-?3|AAC|FLAC|Opus|L?PCM)(?:[^a-z0-9]|$)`)
)

func resAlias(s string) string {
	switch strings.ToUpper(resAliasRe.FindString(s)) {
	case "FHD":
		return "1080P"
	case "UHD":
		return "4K"
	case "QHD":
		return "2K"
	}
	return ""
}

// resRank orders resolutions for the resolution sort; unknown sorts last.
func resRank(res string) int {
	switch res {
	case "4K":
		return 6
	case "2K":
		return 5
	case "1080P":
		return 4
	case "720P":
		return 3
	case "480P":
		return 2
	case "360P":
		return 1
	}
	return 0
}

// parseSize reads a size out of free text. GB is taken as decimal and GiB
// as binary, as written — addons use both.
func parseSize(s string) int64 {
	m := sizeRe.FindStringSubmatch(s)
	if len(m) < 3 {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(m[2]) {
	case "GB":
		n *= 1e9
	case "GIB":
		n *= 1 << 30
	case "MB":
		n *= 1e6
	case "MIB":
		n *= 1 << 20
	}
	return int64(n)
}

func extractHDR(s string) string {
	switch {
	case dvRe.MatchString(s):
		return "DV"
	case hdrPlusRe.MatchString(s):
		return "HDR+"
	case hdrRe.MatchString(s):
		return "HDR"
	}
	return ""
}

// extractDuration gives the runtime in seconds, or 0 if the addon didn't say.
func extractDuration(s string) int {
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	mins, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	return h*3600 + mins*60 + sec
}

// extractAudio finds the first audio format and the spoken languages.
// Languages only come from an addon that lists them; a release name's
// "Dual" or "Multi" doesn't say which.
func extractAudio(text, filename string) (codec string, langs []string) {
	if m := codecLineRe.FindStringSubmatch(text); m != nil {
		codec = strings.TrimSpace(m[1])
	} else if m := codecNameRe.FindStringSubmatch(filename); m != nil {
		codec = normCodec(m[1])
	}

	if m := langLineRe.FindStringSubmatch(text); m != nil {
		seen := map[string]bool{}
		for _, cc := range flagCodes(m[1]) {
			l := flagLang(cc)
			if !seen[l] {
				seen[l] = true
				langs = append(langs, l)
			}
		}
	}
	return codec, langs
}

func normCodec(c string) string {
	u := strings.ToUpper(strings.NewReplacer(".", " ", "_", " ").Replace(c))
	switch {
	case strings.HasPrefix(u, "DTS-HD") && strings.HasSuffix(u, "MA"):
		return "DTS-HD MA"
	case u == "TRUEHD":
		return "TrueHD"
	case u == "DDP", u == "DD+", strings.Contains(u, "E-AC") || u == "EAC3" || u == "EAC-3":
		return "DD+"
	case strings.HasPrefix(u, "AC"):
		return "DD"
	case u == "OPUS":
		return "Opus"
	}
	return u
}

// flagCodes turns flag emoji into their two-letter country codes. A flag is
// a pair of regional indicator symbols, one per letter.
func flagCodes(s string) []string {
	var out []string
	var pend rune
	for _, r := range s {
		if r < 0x1F1E6 || r > 0x1F1FF {
			pend = 0
			continue
		}
		l := 'A' + (r - 0x1F1E6)
		if pend == 0 {
			pend = l
			continue
		}
		out = append(out, string([]rune{pend, l}))
		pend = 0
	}
	return out
}

// flagLang maps the countries addons use as stand-ins for languages back to
// the language. Anything not listed shows as its country code, which is
// usually close enough to read.
func flagLang(cc string) string {
	switch cc {
	case "GB", "US", "AU":
		return "EN"
	case "JP":
		return "JA"
	case "KR":
		return "KO"
	case "CN", "TW", "HK":
		return "ZH"
	case "DK":
		return "DA"
	case "SE":
		return "SV"
	case "GR":
		return "EL"
	case "IL":
		return "HE"
	case "IN":
		return "HI"
	case "UA":
		return "UK"
	case "CZ":
		return "CS"
	case "BR":
		return "PT"
	case "SA":
		return "AR"
	case "VN":
		return "VI"
	case "MY":
		return "MS"
	case "MX":
		return "ES"
	}
	return cc
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
