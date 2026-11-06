package streams

import (
	"regexp"
	"sort"
	"stremio-cliuwu/internal/stremio"
	"strings"
	"unicode"
)

var (
	invisRe  = regexp.MustCompile(`[\x{200b}-\x{200f}\x{2060}-\x{206f}\x{fe0f}\x{00ad}\x{feff}\x{200d}\x{200c}\x{180e}\x{00a0}\x{202a}-\x{202e}\x{2028}\x{2029}]+`)
	ResRe    = regexp.MustCompile(`(?i)\b(4K|2K|2160[Pp]|1440[Pp]|1080[Pp]|720[Pp]|480[Pp]|360[Pp])\b`)
	SourceRe = regexp.MustCompile(`[〈<]([^〉>]+)[〉>]`)
	SizeRe   = regexp.MustCompile(`(?i)(\d+\.?\d*)\s*(GB|MB|GiB|MiB)`)
	StarRe   = regexp.MustCompile(`[★☆✦✧⭐]+`)
)

func StripInvis(s string) string {
	s = invisRe.ReplaceAllString(s, " ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func ExtractRes(s string) string {
	m := ResRe.FindString(s)
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

func ExtractSrc(s string) string {
	m := SourceRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
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

// InLibrary reports whether the stream is already in your debrid
// library: no fetch, no waiting, and a copy you've chosen before.
func InLibrary(s stremio.Stream) bool {
	return libraryRe.MatchString(s.Name + " " + s.Description + " " + s.Title)
}

// Cached classifies a stream: 1 cached, -1 explicitly not cached,
// 0 unknown (a direct HTTP addon, say, where the question doesn't apply).
func Cached(s stremio.Stream) int {
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

// Sort orders the stream list. Cached-first matters more than
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

// Blocked reports whether a stream matches any of your blocked terms.
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
func Blocked(s stremio.Stream, terms []string) bool {
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

// Sort orders by addon priority, then binge group, then library,
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
func Sort(streams []stremio.Stream, preferred, binge string, cachedFirst bool) []stremio.Stream {
	pref := strings.ToUpper(preferred)

	// Keys are worked out once per stream: the library and cache checks are
	// regex matches, and a comparator runs them O(n log n) times over.
	type keyed struct {
		s       stremio.Stream
		binge   bool
		library bool
		cache   int
		quality bool
	}
	ks := make([]keyed, len(streams))
	for i, s := range streams {
		k := keyed{s: s, library: InLibrary(s)}
		k.binge = binge != "" && s.BehaviorHints.BingeGroup == binge
		if cachedFirst {
			k.cache = Cached(s)
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

// AutoPick chooses a stream to play without asking, or says there isn't one
// it would stand behind.
//
// The list is sorted exactly as the picker sorts it, and the top entry is
// taken only if it is a safe bet: the same release as the last episode
// (binge group), a file already in your library, or a cached link. Anything
// else might be a CAM, a dead torrent or a different dub, and guessing wrong
// costs more than showing the list.
func AutoPick(all []stremio.Stream, preferred, binge string, cachedFirst bool, blocked []string) (stremio.Stream, bool) {
	var ok []stremio.Stream
	for _, s := range all {
		if s.URL != "" && !Blocked(s, blocked) {
			ok = append(ok, s)
		}
	}
	if len(ok) == 0 {
		return stremio.Stream{}, false
	}
	top := Sort(ok, preferred, binge, cachedFirst)[0]
	sameRelease := binge != "" && top.BehaviorHints.BingeGroup == binge
	// A link flagged as not cached needs downloading first, whatever release
	// it is, so it is never a safe bet.
	if Cached(top) != -1 && (sameRelease || InLibrary(top) || Cached(top) == 1) {
		return top, true
	}
	return stremio.Stream{}, false
}
