package format

import (
	"fmt"
	"net/url"
	"strconv"
	"stremio-cliuwu/internal/stremio"
	"strings"
	"time"
	"unicode/utf8"
)

// Rating normalises a rating for display. Addons disagree on precision:
// cinemeta sends one decimal, others pass a tmdb score through with three.
func Rating(s string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return s
	}
	return strconv.FormatFloat(f, 'f', 1, 64)
}

func Secs(s float64) string {
	if s < 0 {
		s = 0
	}
	t := int(s)
	h, m, sec := t/3600, (t%3600)/60, t%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// ParseRelease reads the assorted date formats the metadata addons emit.
func ParseRelease(s string) (time.Time, bool) {
	if s == "" || s == "N/A" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"2006-01-02",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"02 Jan 2006",
		"Jan 2, 2006",
		time.RFC3339,
	} {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		// Reject the Unix epoch — means the field was unset
		if t.IsZero() || (t.Year() == 1970 && t.Month() == 1 && t.Day() == 1) {
			return time.Time{}, false
		}
		return t, true
	}
	return time.Time{}, false
}

// dateFormats are the layouts offered in settings. 02/01 and 01/02 are
// indistinguishable for the first twelve days of a month, so which one you're
// looking at can't be inferred from the output — it has to be a setting.
var dateFormats = []string{"dmy", "mdy", "ymd", "long"}

// DateFormatName is what settings shows, since "dmy" tells you the field
// order but not which convention it belongs to.
func DateFormatName(kind string) string {
	switch kind {
	case "mdy":
		return "US"
	case "ymd":
		return "ISO"
	case "long":
		return "written"
	}
	return "European"
}

func DateLayout(kind string) string {
	switch kind {
	case "mdy":
		return "Mon 01/02/06"
	case "ymd":
		return "Mon 2006-01-02"
	case "long":
		return "Mon 2 Jan 2006"
	}
	return "Mon 02/01/06"
}

func NextDateFormat(cur string) string {
	for i, f := range dateFormats {
		if f == cur {
			return dateFormats[(i+1)%len(dateFormats)]
		}
	}
	return "dmy"
}

// DateSample renders today in a layout, for the settings preview.
func DateSample(kind string) string { return time.Now().Format(DateLayout(kind)) }

// daysUntil is how many whole days away a release date is, or -1 if it's
// already passed or unparseable.
// daysUntil counts calendar days, not 24-hour periods.
//
// Dividing elapsed hours by 24 answers a different question: at 6am on the
// 7th, something airing on the 9th is 42 hours away, and 42/24 is 1 — so it
// reported "tomorrow" for the day after tomorrow. What you want to know is
// how many midnights are between now and then.
//
// Local time on both sides: release timestamps are UTC, and the day an
// episode lands on is the day it lands on where you are.
func daysUntil(released string) int {
	t, ok := ParseRelease(released)
	if !ok {
		return -1
	}

	// The air date is taken exactly as published, not converted.
	//
	// These dates are editorial, not timestamps: the metadata databases
	// record the broadcast day in the show's origin country, and file an
	// episode airing at 00:30 under the previous evening because that's the
	// day the listings say. The time component is frequently invented to
	// fill the field. Shifting such a date into another zone moves it a day
	// for anyone west of UTC and corrupts the one part that was meaningful.
	//
	// Today, by contrast, genuinely is local — it's a fact about you rather
	// than a published label.
	//
	// Both are then rebuilt as UTC midnights, where every day is exactly 24
	// hours, so the subtraction is exact and no clock change can skew it.
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)

	days := int(day.Sub(today) / (24 * time.Hour))
	if days < 0 {
		return -1
	}
	return days
}

// untilParts splits a day count into two units — a month and a bit reads
// badly as either "in a month" (loses two weeks) or "in 43 days" (makes you
// do the arithmetic). Months are approximated at 30 days, which is close
// enough for an air date and avoids calendar edge cases.
func untilParts(days int) (n1 int, u1 string, n2 int, u2 string) {
	switch {
	case days < 7:
		return days, "day", 0, ""
	case days < 31:
		return days / 7, "week", days % 7, "day"
	case days < 365:
		return days / 30, "month", (days % 30) / 7, "week"
	}
	return days / 365, "year", (days % 365) / 30, "month"
}

func Plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// UntilRelease is the long form, for the info panel.
func UntilRelease(released string) string {
	days := daysUntil(released)
	switch {
	case days < 0:
		return ""
	case days < 1:
		return "today"
	case days == 1:
		return "tomorrow"
	}

	n1, u1, n2, u2 := untilParts(days)
	out := "in " + Plural(n1, u1)
	if n2 > 0 {
		out += ", " + Plural(n2, u2)
	}
	return out
}

// VideoAired reports whether an episode's release date has passed. An
// unparseable or missing date counts as aired — plenty of catalogs leave it
// blank, and refusing to play those would be worse than the occasional
// unaired entry slipping through.
func VideoAired(v stremio.Video) bool {
	t, ok := ParseRelease(v.Released)
	if !ok {
		return true
	}
	return !t.After(time.Now())
}

// SpecialGroup is one collection inside the specials season — an OVA, a
// recap, a set of shorts. Each is its own title in kitsu.
type SpecialGroup struct {
	KitsuID string
	Title   string

	// Filled in from the collection's own meta, not counted here: the merged
	// series stamps one kitsu id across more videos than that entry holds.
	Count int
}

// GroupSpecials splits season 0 by the kitsu entry each video came from,
// keeping the order they appear in.
//
// Season 0 of a merged anime is a dumping ground: forty-odd unrelated shorts
// from half a dozen different releases. They arrive tagged with which release
// they belong to, so the list can offer those instead.
func GroupSpecials(videos []stremio.Video) []SpecialGroup {
	var out []SpecialGroup
	at := map[string]int{}
	names := map[string][]string{}

	for _, v := range videos {
		if v.Season != 0 || v.KitsuID == "" {
			continue
		}
		if _, ok := at[v.KitsuID]; !ok {
			at[v.KitsuID] = len(out)
			out = append(out, SpecialGroup{KitsuID: v.KitsuID})
		}
		names[v.KitsuID] = append(names[v.KitsuID], v.Title)
	}

	for i := range out {
		out[i].Title = groupTitle(names[out[i].KitsuID])
	}
	return out
}

// groupTitle is what a collection's episode names have in common, cut back to
// something that reads as a title — a placeholder until the real name is
// fetched, and the fallback if that fails.
func groupTitle(names []string) string {
	switch len(names) {
	case 0:
		return "specials"
	case 1:
		return names[0]
	}

	prefix := names[0]
	for _, n := range names[1:] {
		prefix = CommonPrefix(prefix, n)
		if prefix == "" {
			break
		}
	}

	// "No Regrets Part 1" and "Part 2" share "No Regrets Part", so the
	// numbering word comes off the end. OVA and Special stay: they're part
	// of the title, not a counter.
	enumerators := map[string]bool{
		"part": true, "day": true, "episode": true, "ep": true, "pt": true,
		"vol": true, "volume": true, "chapter": true, "act": true, "no": true,
	}

	const cut = " ~-–—:·,(&"
	prefix = strings.TrimRight(prefix, cut)

	for range 2 {
		i := strings.LastIndexAny(prefix, " ")
		if i <= 0 {
			break
		}
		last := strings.ToLower(strings.Trim(prefix[i+1:], cut))
		partial := len(prefix) < len(names[0]) && names[0][len(prefix)] != ' '
		if !enumerators[last] && !partial {
			break
		}
		prefix = strings.TrimRight(prefix[:i], cut)
	}

	if prefix == "" {
		return names[0]
	}
	return prefix
}

func CommonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	// Byte-wise match can stop inside a multi-byte character; back up to its start.
	for i > 0 && i < len(a) && !utf8.RuneStart(a[i]) {
		i--
	}
	return a[:i]
}

// noSeason means "open the show, no particular season". Zero can't do that
// job any more — it's the specials season, and a real destination.
const NoSeason = -1

// IndexOfSeason finds a season's row, or the first row if it isn't there.
func IndexOfSeason(seasons []int, want int) int {
	for i, s := range seasons {
		if s == want {
			return i
		}
	}
	return 0
}

// SeasonLabel names a season row. Season 0 is where addons file specials,
// OVAs and recaps.
func SeasonLabel(season int) string {
	if season == 0 {
		return "specials"
	}
	return fmt.Sprintf("season %d", season)
}

// Ep labels an episode from its stored numbers, falling back to parsing
// the id.
//
// Parsing is the unreliable path: a kitsu id carries no season at all, so
// VideoID has to assume 1 — which is right for a first cour and wrong for
// every one after it. Anywhere the season and episode are already known, use
// them.
func Ep(season, episode int, videoID string) string {
	// season 0 is legitimate — it's where specials live — so the episode
	// number is what decides whether this is an episode at all.
	if episode > 0 && season >= 0 {
		return fmt.Sprintf("S%02dE%02d", season, episode)
	}
	return VideoID(videoID)
}

// VideoID turns "tt1234:2:5" or "kitsu:123:7" into "S02E05".
func VideoID(videoID string) string {
	parts := strings.Split(videoID, ":")
	if len(parts) >= 3 {
		sStr, eStr := parts[len(parts)-2], parts[len(parts)-1]
		if parts[0] == "kitsu" && len(parts) == 3 {
			sStr = "1"
			eStr = parts[2]
		}
		s, se := strconv.Atoi(sStr)
		e, ee := strconv.Atoi(eStr)
		if se == nil && ee == nil {
			return fmt.Sprintf("S%02dE%02d", s, e)
		}
	}
	return videoID
}

// CleanTitle un-escapes and de-extensions an mpv media-title.
func CleanTitle(s string) string {
	if s == "" {
		return ""
	}
	if once, err := url.QueryUnescape(s); err == nil {
		s = once
	}
	if twice, err := url.QueryUnescape(s); err == nil {
		s = twice
	}
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".m4v", ".ts", ".mov", ".wmv"} {
		if strings.HasSuffix(strings.ToLower(s), ext) {
			s = s[:len(s)-len(ext)]
			break
		}
	}
	return strings.TrimSpace(s)
}

// KnownKind reports whether a value is one of the buckets sourceTag renders.
func KnownKind(s string) bool {
	switch s {
	case "movie", "show", "anime", "other":
		return true
	}
	return false
}

// ProgressGlyph renders a compact "12:34 / 45:00 (27%)" style readout.
func ProgressGlyph(pos, dur float64) string {
	if dur <= 0 {
		return Secs(pos)
	}
	return fmt.Sprintf("%s / %s (%.0f%%)", Secs(pos), Secs(dur), pos/dur*100)
}

// NaturalLess compares strings with embedded numbers the way a person would:
// "S01E02" before "S01E10", "Season 2" before "Season 10". A plain string
// compare puts E10 before E2, which is why a season pack comes out shuffled.
func NaturalLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0

	for i < len(la) && j < len(lb) {
		ca, cb := la[i], lb[j]
		da, db := ca >= '0' && ca <= '9', cb >= '0' && cb <= '9'

		if da && db {
			// Consume both runs of digits and compare them as numbers.
			si, sj := i, j
			for i < len(la) && la[i] >= '0' && la[i] <= '9' {
				i++
			}
			for j < len(lb) && lb[j] >= '0' && lb[j] <= '9' {
				j++
			}
			na := strings.TrimLeft(la[si:i], "0")
			nb := strings.TrimLeft(lb[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb) // fewer digits = smaller number
			}
			if na != nb {
				return na < nb
			}
			continue
		}

		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(la)-i < len(lb)-j
}

// ── Completion thresholds ─────────────────────────────────────────────────────
//
// A flat percentage treats every runtime the same, which breaks badly at the
// extremes: 70% of a two and a half hour film leaves 45 minutes unwatched, and
// 5% of a ten minute cartoon is half a minute.
//
// The approach the established players take is a percentage bounded by
// absolute limits — Kodi pairs playcountminimumpercent with a flat
// ignoresecondsatstart of 180, Jellyfin pairs MinResumePct with
// MinResumeDurationSeconds. Same idea here.
//
// Deliberately generous at the end: film credits regularly run ten minutes,
// and stopping when they start should count as having watched the thing.

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// watchedAllowance is how much runtime may remain while still counting as
// watched — 14%, bounded to 25s…18min. That puts most things at 86% and a
// feature-length film nearer 88%, since the cap only starts to bite past
// about two hours.
func watchedAllowance(dur float64) float64 {
	return clampF(0.14*dur, 25, 18*60)
}

// resumeFloor is how far in you must be before a resume point is worth
// keeping — 5%, bounded to 15s…3min. Below it, you've barely started.
func resumeFloor(dur float64) float64 {
	return clampF(0.05*dur, 15, 3*60)
}

// PrefetchLead is how long before the end to start resolving the next episode.
// Enough time for a debrid addon to answer, without jumping the gun on a film.
func PrefetchLead(dur float64) float64 {
	return clampF(0.15*dur, 90, 6*60)
}

// IsWatchedAt reports whether a position counts as having finished the video.
func IsWatchedAt(pos, dur float64) bool {
	// pos 0 is "not started": a short or still-loading duration would
	// otherwise satisfy the allowance on its own and mark the video watched.
	if dur <= 0 || pos <= 0 {
		return false
	}
	return dur-pos <= watchedAllowance(dur)
}

// HasResumePoint reports whether a position is worth resuming from: far enough
// in to be meaningful, not so far in that it's effectively finished.
func HasResumePoint(pos, dur float64) bool {
	if dur <= 0 || pos <= 0 {
		return false
	}
	return pos >= resumeFloor(dur) && !IsWatchedAt(pos, dur)
}
