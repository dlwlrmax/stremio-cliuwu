package main

import (
	"sort"
	"stremio-cliuwu/internal/format"
	"stremio-cliuwu/internal/stremio"
)

// Item is a single row in any list screen.
type Item struct {
	Label   string // primary text
	Sub     string // dimmed secondary text
	Badge   string // right-aligned tag e.g. "[movie]" "S02 · 4/12"
	Watched bool   // prefix with green ✓
	Dim     bool   // grey out entire row
	Header  bool   // section label or blank spacer — not selectable
}

func (i Item) selectable() bool { return !i.Header }

// ── Episode queue ─────────────────────────────────────────────────────────────

// EpQueue is the season context handed to the player so it can advance to the
// next episode itself. With loadfile/replace there is no mpv playlist to lean
// on, so autoplay lives here instead.
type EpQueue struct {
	Show     stremio.Meta
	Season   int
	Episodes []stremio.Video // this season only
	Index    int

	// All is every episode of the series. Carrying it means the end of a
	// season isn't the end of the queue — without it, finishing a finale
	// looked identical to finishing the show.
	All []stremio.Video
}

func (q *EpQueue) HasPrev() bool { return q != nil && q.Index > 0 }

func (q *EpQueue) HasNext() bool {
	_, _, ok := q.Next()
	return ok
}

// Next returns the following episode and its season, rolling over into the
// next season when the current one runs out.
// Next is the episode to play after this one, or nothing when it isn't out
// yet — autoplay and prefetch both depend on that.
func (q *EpQueue) Next() (stremio.Video, int, bool) {
	v, season, ok := q.Upcoming()
	if !ok || !format.VideoAired(v) {
		return stremio.Video{}, 0, false
	}
	return v, season, true
}

// Upcoming is the episode after this one whether or not it has aired.
//
// Split from Next because the two questions differ: you can't play an episode
// that isn't out, but you still want to be told it's coming. Folding them
// together made a season read as finished the moment you caught up with it.
func (q *EpQueue) Upcoming() (stremio.Video, int, bool) {
	if q == nil {
		return stremio.Video{}, 0, false
	}
	if q.Index+1 < len(q.Episodes) {
		return q.Episodes[q.Index+1], q.Season, true
	}

	// Lowest season above this one…
	next := -1
	for _, v := range q.All {
		if v.Season > q.Season && (next == -1 || v.Season < next) {
			next = v.Season
		}
	}
	if next == -1 {
		return stremio.Video{}, 0, false
	}

	// …and its earliest episode.
	var first stremio.Video
	found := false
	for _, v := range q.All {
		if v.Season == next && (!found || v.Episode < first.Episode) {
			first, found = v, true
		}
	}
	return first, next, found
}

// SeasonEpisodes pulls one season's episodes out of All, in order.
func (q *EpQueue) SeasonEpisodes(season int) []stremio.Video {
	var eps []stremio.Video
	for _, v := range q.All {
		if v.Season == season {
			eps = append(eps, v)
		}
	}
	sort.Slice(eps, func(a, b int) bool { return eps[a].Episode < eps[b].Episode })
	return eps
}

// ── Config ────────────────────────────────────────────────────────────────────
