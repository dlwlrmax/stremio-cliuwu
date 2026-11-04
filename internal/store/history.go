package store

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strconv"
	"stremio-cliuwu/internal/bitfield"
	"stremio-cliuwu/internal/format"
	"stremio-cliuwu/internal/jsonio"
	"stremio-cliuwu/internal/paths"
	"stremio-cliuwu/internal/stremio"
	"strings"
	"sync"
	"time"
)

// History is held in memory and flushed on a timer.
//
// The old shape was a flat array of fully-populated entries, rewritten whole
// on every position update — once a second during playback — and re-parsed by
// every reader. Marking a 300-episode season watched wrote 300 records that
// each repeated the show's name, id, type, source and year plus the next-up
// fields, to express one bit per episode, and then evicted the rest of the
// history under the entry cap.
//
// Now a show is stored once and an episode carries only what differs from the
// default: 19 bytes for a watched one. Reads never touch disk, and writes are
// debounced, turning ~1300 KB/s into about 6 KB every fifteen seconds.

const (
	flushDelay    = 15 * time.Second
	defaultRecent = 300 // fallback when the setting is unreadable
	HistoryVer    = 3
)

// recentLimit is how many entries the history screen keeps. Watched state is
// unbounded and lives separately — this only caps the list you browse.
func recentLimit() int {
	if n := CfgNow().HistoryMax; n > 0 {
		return n
	}
	return defaultRecent
}

// ── On-disk shape ─────────────────────────────────────────────────────────────

// EpRecord is one episode. Short keys and omitempty throughout: this is the
// part that repeats thousands of times.
type EpRecord struct {
	W bool    `json:"w,omitempty"` // watched
	P float64 `json:"p,omitempty"` // position, seconds
	D float64 `json:"d,omitempty"` // duration, seconds
	T int64   `json:"t,omitempty"` // last touched, unix seconds

	// M is the last real state change: marked watched, an episode opened, a
	// watched threshold crossed. T is rewritten every playback second, which
	// makes it useless as a last-write-wins clock for syncing; M only moves
	// when something actually changed.
	M int64 `json:"m,omitempty"`
}

// ShowState holds a title's details once, with its episodes beneath.
type ShowState struct {
	Name   string `json:"name"`
	Type   string `json:"type,omitempty"`
	Source string `json:"source,omitempty"`
	Year   string `json:"year,omitempty"`
	SeenAt int64  `json:"seen_at,omitempty"`

	// Poster is optional and only ever arrives from Stremio. Old files load
	// fine without it, so the format version doesn't move.
	Poster string `json:"poster,omitempty"`

	// The addon that served a library entry. Kept on the show rather than
	// on a recent row, because recent rows age out and the entry is still
	// openable from the grouped view long afterwards.
	Base string `json:"base,omitempty"`

	// Keyed "season:episode". Movies use "0:0".
	Eps map[string]*EpRecord `json:"eps,omitempty"`
}

// RecentEntry is one line of the history screen.
//
// Deliberately separate from watched state: bulk-marking a season writes
// hundreds of episode states but nothing here, because marking isn't
// watching, and it used to flush the entire history list off the end.
type RecentEntry struct {
	ShowID  string `json:"id"`
	Season  int    `json:"s,omitempty"`
	Episode int    `json:"e,omitempty"`
	VideoID string `json:"v,omitempty"`
	EpTitle string `json:"t,omitempty"`
	Total   int    `json:"n,omitempty"` // episodes in that season
	At      int64  `json:"at"`

	// What follows this episode, recorded at play time.
	//
	// EpQueue has the real video list at that moment, including the next
	// season, so this is exact. Working it out later from Episode+1 can't
	// know a season has five episodes and can't roll over — which is how a
	// finished season came to offer S28E06. An empty NextVideoID means there
	// genuinely is nothing after this one.
	NextVideoID string `json:"nv,omitempty"`
	NextSeason  int    `json:"ns,omitempty"`
	NextEpisode int    `json:"ne,omitempty"`
	NextTitle   string `json:"nt,omitempty"`
	NextRelease string `json:"nr,omitempty"`
}

type HistoryFile struct {
	Version int                   `json:"version"`
	Shows   map[string]*ShowState `json:"shows"`
	Recent  []RecentEntry         `json:"recent"`
}

// ── In-memory store ───────────────────────────────────────────────────────────

// EpRef locates an episode's state without a second lookup.
type EpRef struct {
	showID string
	key    string
}

type HistoryStore struct {
	mu   sync.Mutex
	fmu  sync.Mutex // serialises Flush end to end so writes land in order
	data HistoryFile

	byVideo map[string]EpRef
	loaded  bool
	dirty   bool
	pending bool // a flush is already scheduled
}

var Hist = &HistoryStore{}

func EpKey(season, episode int) string {
	return strconv.Itoa(season) + ":" + strconv.Itoa(episode)
}

func parseEpKey(k string) (season, episode int) {
	if i := strings.IndexByte(k, ':'); i > 0 {
		season, _ = strconv.Atoi(k[:i])
		episode, _ = strconv.Atoi(k[i+1:])
	}
	return
}

// load reads the file once. Callers hold the lock.
func (h *HistoryStore) load() {
	if h.loaded {
		return
	}
	h.loaded = true
	h.data = HistoryFile{Version: HistoryVer, Shows: map[string]*ShowState{}}
	h.byVideo = map[string]EpRef{}

	b, err := os.ReadFile(paths.HistFile())
	if err != nil {
		return
	}

	// The v1 flat array is deliberately not read — if that ever needs moving
	// across, a standalone script can do it once rather than the app carrying
	// a migration path forever. v2 → v3 added the real mtime M.
	var f HistoryFile
	if json.Unmarshal(b, &f) != nil || f.Version < 2 || f.Shows == nil {
		// Starting empty is fine; overwriting the file on the next flush is
		// not. Keep what was there.
		jsonio.Quarantine(paths.HistFile())
		return
	}
	if f.Version < HistoryVer {
		for _, sh := range f.Shows {
			for _, e := range sh.Eps {
				if e.M == 0 {
					e.M = e.T
				}
			}
		}
		f.Version = HistoryVer
	}

	h.data = f
	if h.data.Shows == nil {
		h.data.Shows = map[string]*ShowState{}
	}
	h.reindex()
}

// reindex rebuilds the videoID lookup after loading.
func (h *HistoryStore) reindex() {
	h.byVideo = map[string]EpRef{}
	for _, sh := range h.data.Shows {
		if sh.Eps == nil {
			sh.Eps = map[string]*EpRecord{}
		}
	}
	for _, r := range h.data.Recent {
		if r.VideoID != "" {
			h.byVideo[r.VideoID] = EpRef{showID: r.ShowID, key: EpKey(r.Season, r.Episode)}
		}
	}
}

func (h *HistoryStore) show(id string, m stremio.Meta) *ShowState {
	sh, ok := h.data.Shows[id]
	if !ok {
		sh = &ShowState{Eps: map[string]*EpRecord{}}
		h.data.Shows[id] = sh
	}
	if sh.Eps == nil {
		sh.Eps = map[string]*EpRecord{}
	}
	if m.Name != "" {
		sh.Name = m.Name
	}
	if m.Type != "" {
		sh.Type = m.Type
	}
	if m.Source != "" {
		sh.Source = m.Source
	}
	if m.Year != "" {
		sh.Year = m.Year
	}
	if m.Base != "" {
		sh.Base = m.Base
	}
	return sh
}

func (h *HistoryStore) ep(showID, key string) *EpRecord {
	if sh, ok := h.data.Shows[showID]; ok {
		return sh.Eps[key]
	}
	return nil
}

func (h *HistoryStore) trimRecent() {
	if n := recentLimit(); len(h.data.Recent) > n {
		clear(h.data.Recent[n:])
		h.data.Recent = h.data.Recent[:n]
	}
}

// touch marks the store dirty and schedules a flush, if one isn't already due.
func (h *HistoryStore) touch() {
	h.dirty = true
	InvalidateInProgress()

	if h.pending {
		return
	}
	h.pending = true
	go func() {
		time.Sleep(flushDelay)
		Hist.Flush()
	}()
}

// Flush writes the store out if anything changed.
func (h *HistoryStore) Flush() {
	h.fmu.Lock()
	defer h.fmu.Unlock()
	h.mu.Lock()
	h.pending = false
	if !h.dirty || !h.loaded {
		h.mu.Unlock()
		return
	}
	h.dirty = false
	h.data.Version = HistoryVer
	b, err := json.Marshal(h.data)
	h.mu.Unlock()

	if err == nil {
		err = jsonio.WriteAtomic(paths.HistFile(), b, 0600)
	}
	if err != nil {
		// Still unsaved: mark it so the next change or exit tries again.
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
	}
}

// WarmHistory parses the file off the critical path. Without it the first
// read happens while the first frame is being drawn, which is the one moment
// it's visible.
func WarmHistory() {
	go func() {
		Hist.mu.Lock()
		defer Hist.mu.Unlock()
		Hist.load()
	}()
}

// FlushHistory is called on the way out so the last few seconds aren't lost.
func FlushHistory() { Hist.Flush() }

// ── In-progress cache ─────────────────────────────────────────────────────────

var (
	inProgressCache *ContinueItem
	inProgressValid bool
)

func InvalidateInProgress() { inProgressValid = false }

// ── Episode state ─────────────────────────────────────────────────────────────

// EpState is one episode's recorded state.
type EpState struct {
	Position float64
	Duration float64
	Watched  bool
}

// EpisodeStates returns every recorded episode for a show — a map lookup now,
// where it used to be a file read and a linear scan.
func EpisodeStates(showID string) map[[2]int]EpState {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	out := map[[2]int]EpState{}
	sh, ok := Hist.data.Shows[showID]
	if !ok {
		return out
	}
	for k, e := range sh.Eps {
		season, episode := parseEpKey(k)
		// Season 0 is the specials season and a real destination; only a film
		// (0:0) has no episode to report.
		if season < 0 || episode <= 0 {
			continue
		}
		out[[2]int{season, episode}] = EpState{Position: e.P, Duration: e.D, Watched: e.W}
	}
	return out
}

func GetPosition(videoID string) (pos, duration float64, watched bool) {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	if ref, ok := Hist.byVideo[videoID]; ok {
		if e := Hist.ep(ref.showID, ref.key); e != nil {
			return e.P, e.D, e.W
		}
	}
	return 0, 0, false
}

func GetPositionByEpisode(showID string, season, episode int) (pos, duration float64, watched bool) {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	if e := Hist.ep(showID, EpKey(season, episode)); e != nil {
		return e.P, e.D, e.W
	}
	return 0, 0, false
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// AddHistory records that something was opened. maxEntries is kept in the
// signature for callers; the recent list has its own bound.
func AddHistory(e HistoryEntry, maxEntries int) {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	sh := Hist.show(e.ID, stremio.Meta{Name: e.Name, Type: e.Type, Source: e.Source, Year: e.Year, Base: e.Base})
	now := time.Now().Unix()
	sh.SeenAt = now

	key := EpKey(e.Season, e.Episode)
	if sh.Eps[key] == nil {
		sh.Eps[key] = &EpRecord{}
	}
	sh.Eps[key].T = now
	sh.Eps[key].M = now

	if e.VideoID != "" {
		Hist.byVideo[e.VideoID] = EpRef{showID: e.ID, key: key}
	}

	Hist.pushRecent(e, now)
	Hist.touch()
}

// pushRecent moves an entry to the front of the recent list, dropping any
// earlier row for the same episode. Callers hold the lock.
func (h *HistoryStore) pushRecent(e HistoryEntry, now int64) {
	out := h.data.Recent[:0]
	for _, r := range h.data.Recent {
		if r.ShowID == e.ID && r.Season == e.Season && r.Episode == e.Episode {
			continue
		}
		out = append(out, r)
	}

	// No clear needed here: the append below builds a new slice, so the old
	// backing array goes unreachable whole.
	h.data.Recent = append([]RecentEntry{{
		ShowID: e.ID, Season: e.Season, Episode: e.Episode,
		VideoID: e.VideoID, EpTitle: e.EpTitle, Total: e.EpisodeTotal, At: now,
		NextVideoID: e.NextVideoID, NextSeason: e.NextSeason,
		NextEpisode: e.NextEpisode, NextTitle: e.NextTitle,
		NextRelease: e.NextReleased,
	}}, out...)

	h.trimRecent()
}

// ensureRecent inserts a history row for a title if one isn't already there,
// keeping the list newest-first. Merging remote progress needs this: a pulled
// item has show/episode state but no recent row, so it wouldn't appear on the
// continue rails. Unlike pushRecent it never moves an existing row, so a pull
// can't reorder what you watched here. Callers hold the lock.
func (h *HistoryStore) ensureRecent(e HistoryEntry, at int64) {
	for _, r := range h.data.Recent {
		if r.ShowID == e.ID && r.Season == e.Season && r.Episode == e.Episode {
			return
		}
	}

	i := 0
	for i < len(h.data.Recent) && h.data.Recent[i].At >= at {
		i++
	}
	h.data.Recent = append(h.data.Recent, RecentEntry{})
	copy(h.data.Recent[i+1:], h.data.Recent[i:])
	h.data.Recent[i] = RecentEntry{
		ShowID: e.ID, Season: e.Season, Episode: e.Episode,
		VideoID: e.VideoID, EpTitle: e.EpTitle, Total: e.EpisodeTotal, At: at,
	}
	h.trimRecent()
}

// UpdatePosition records playback progress. Called about once a second, so it
// only mutates memory — the write is on a timer.
func UpdatePosition(videoID string, pos, duration float64) {
	if videoID == "" || duration <= 0 {
		return
	}

	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	ref, ok := Hist.byVideo[videoID]
	if !ok {
		return
	}
	e := Hist.ep(ref.showID, ref.key)
	if e == nil {
		return
	}

	e.P, e.D, e.T = pos, duration, time.Now().Unix()
	// Only a genuine change to the watched flag moves the sync clock. The
	// per-second position ticks above keep updating P/D/T alone.
	if !e.W && format.IsWatchedAt(pos, duration) {
		e.W = true
		e.M = e.T
	}
	Hist.touch()
}

// MarkPositionFinal stamps an episode's sync clock M after its last position
// write. UpdatePosition deliberately leaves M alone on the per-second ticks —
// M is the last-write-wins clock and moving it every second would make the
// sync clock meaningless — but that also means a file that just ended still
// carries the M from when it was opened. The final P/D would then be pushed
// under a stale M and lose to an older server copy. Call this after the last
// UpdatePosition and before the push reads local state.
func MarkPositionFinal(videoID string) {
	if videoID == "" {
		return
	}

	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	ref, ok := Hist.byVideo[videoID]
	if !ok {
		return
	}
	if e := Hist.ep(ref.showID, ref.key); e != nil {
		e.M = time.Now().Unix()
	}
	Hist.touch()
}

// SetWatchedByEpisode marks an episode watched or unwatched, creating the
// record if there isn't one.
func SetWatchedByEpisode(e HistoryEntry, watched bool) {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	sh := Hist.show(e.ID, stremio.Meta{Name: e.Name, Type: e.Type, Source: e.Source, Year: e.Year, Base: e.Base})
	key := EpKey(e.Season, e.Episode)

	st := sh.Eps[key]
	if st == nil {
		if !watched {
			return // nothing recorded, nothing to unmark
		}
		st = &EpRecord{}
		sh.Eps[key] = st
	}

	now := time.Now().Unix()
	st.W = watched
	st.T = now
	st.M = now
	if watched {
		st.P = st.D
	} else {
		st.P = 0
	}
	if e.VideoID != "" {
		Hist.byVideo[e.VideoID] = EpRef{showID: e.ID, key: key}
	}

	// Marking one episode is a deliberate act about that episode, so it
	// belongs in history the same as watching it would. Bulk marking is the
	// case that mustn't flood the list — see SetSeasonWatched.
	if watched {
		sh.SeenAt = now
		Hist.pushRecent(e, now)
	}
	Hist.touch()
}

func ToggleWatchedByEpisode(e HistoryEntry) bool {
	_, _, watched := GetPositionByEpisode(e.ID, e.Season, e.Episode)
	SetWatchedByEpisode(e, !watched)
	return !watched
}

// SetSeasonWatched applies a state to a whole season in one pass.
//
// This is what the old format handled worst: 300 episodes meant 300 full
// records and 300 lines of history. Now it's 300 × 19 bytes of state and no
// recent entries at all.
func SetSeasonWatched(show stremio.Meta, season int, eps []stremio.Video, watched bool) {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	sh := Hist.show(show.ID, show)
	now := time.Now().Unix()

	for _, v := range eps {
		key := EpKey(season, v.Episode)
		st := sh.Eps[key]
		if st == nil {
			if !watched {
				continue
			}
			st = &EpRecord{}
			sh.Eps[key] = st
		}

		st.W = watched
		st.T = now
		st.M = now
		if watched {
			st.P = st.D
		} else {
			st.P = 0
		}
		if v.ID != "" {
			Hist.byVideo[v.ID] = EpRef{showID: show.ID, key: key}
		}
	}

	// One row for the season, not one per episode: marking twelve episodes
	// shouldn't push eleven other shows off the history screen. The last
	// episode stands for the lot, and carries no next-episode pointer —
	// nothing here knows what follows the season.
	if watched && len(eps) > 0 {
		last := eps[len(eps)-1]
		for _, v := range eps {
			if v.Episode > last.Episode {
				last = v
			}
		}
		sh.SeenAt = now
		Hist.pushRecent(HistoryEntry{
			Name: show.Name, ID: show.ID, Type: show.Type,
			Source: show.Source, Year: show.Year,
			Season: season, Episode: last.Episode, VideoID: last.ID,
			EpTitle: last.Title, EpisodeTotal: len(eps),
		}, now)
	}
	Hist.touch()
}

// ── Outbound snapshot ─────────────────────────────────────────────────────────

// LocalLibraryEntry is one title's local watch state, flat enough to be turned
// into an outbound Stremio library item. The episode chosen is whichever one
// was touched most recently.
type LocalLibraryEntry struct {
	ID       string
	Name     string
	Type     string
	Poster   string
	Bitfield string // per-episode watched state; series only
	Season   int
	Episode  int
	Position float64
	Duration float64
	Watched  bool
	MTime    int64
}

// LocalLibrary snapshots every title with recorded state for pushing out.
func LocalLibrary() []LocalLibraryEntry {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	out := make([]LocalLibraryEntry, 0, len(Hist.data.Shows))
	for id, sh := range Hist.data.Shows {
		var best *EpRecord
		bestKey := ""
		for k, e := range sh.Eps {
			if best == nil || epMTime(e) > epMTime(best) {
				best, bestKey = e, k
			}
		}
		if best == nil {
			continue
		}
		season, episode := parseEpKey(bestKey)
		m := epMTime(best)
		if m == 0 {
			m = sh.SeenAt
		}
		var bf string
		if episode > 0 {
			bf = WatchedBitfield(id, sh)
		}
		out = append(out, LocalLibraryEntry{
			ID: id, Name: sh.Name, Type: sh.Type,
			Poster: sh.Poster, Bitfield: bf,
			Season: season, Episode: episode,
			Position: best.P, Duration: best.D, Watched: best.W,
			MTime: m,
		})
	}
	return out
}

// WatchedBitfield renders a show's local watched episodes as Stremio's
// per-episode bitfield. The index space is the episode order by season and
// episode — the cached meta when the title has been browsed, otherwise the
// episodes the store has recorded. A show known only sparsely can therefore
// express only the episodes it has touched; one never recorded here has no
// index to send. Callers hold the lock.
func WatchedBitfield(showID string, sh *ShowState) string {
	eps := orderedEpisodes(showID, sh)
	if len(eps) == 0 {
		return ""
	}

	// Without the show's full episode list the local order has gaps, and
	// stremio-core places every bit by its distance from the anchor: a gap
	// shrinks that distance and ticks the wrong episodes on the other side.
	// The anchor alone is always placed correctly, so that is all a sparse
	// list may send.
	if len(epsFromMeta(showID)) == 0 {
		last := -1
		for i, oe := range eps {
			if st := sh.Eps[EpKey(oe[0], oe[1])]; st != nil && st.W {
				last = i
			}
		}
		if last < 0 {
			return bitfield.Encode(nil, nil)
		}
		eps = eps[last : last+1]
	}

	videos := make([]string, len(eps))
	watched := map[string]bool{}
	for i, oe := range eps {
		v := StremioVideoID(showID, oe[0], oe[1])
		videos[i] = v
		if st := sh.Eps[EpKey(oe[0], oe[1])]; st != nil && st.W {
			watched[v] = true
		}
	}
	return bitfield.Encode(videos, watched)
}

// epMTime is an episode's real last-write clock, falling back to the old T for
// records written before M existed.
func epMTime(e *EpRecord) int64 {
	if e == nil {
		return 0
	}
	if e.M != 0 {
		return e.M
	}
	return e.T
}

// ── History screen ────────────────────────────────────────────────────────────

// LoadHistory renders the recent list as the flat entries the UI expects.
func LoadHistory() HistoryList {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()
	return Hist.recentEntries()
}

// recentEntries is the unlocked form, so callers already holding the lock
// don't have to drop it and retake it — a pattern that works until someone
// edits it and then deadlocks.
func (h *HistoryStore) recentEntries() HistoryList {
	out := HistoryList{Items: make([]HistoryEntry, 0, len(h.data.Recent))}
	for _, r := range h.data.Recent {
		sh := h.data.Shows[r.ShowID]
		if sh == nil {
			continue
		}
		e := HistoryEntry{
			Name: sh.Name, ID: r.ShowID, Type: sh.Type, Source: sh.Source, Year: sh.Year,
			Season: r.Season, Episode: r.Episode, VideoID: r.VideoID, EpTitle: r.EpTitle,
			Base:         sh.Base,
			EpisodeTotal: r.Total, WatchedAt: time.Unix(r.At, 0),
			NextVideoID: r.NextVideoID, NextSeason: r.NextSeason,
			NextEpisode: r.NextEpisode, NextTitle: r.NextTitle,
			NextReleased: r.NextRelease,
		}
		if st := sh.Eps[EpKey(r.Season, r.Episode)]; st != nil {
			e.Position, e.Duration, e.Watched = st.P, st.D, st.W
		}
		out.Items = append(out.Items, e)
	}
	return out
}

// ShowSummary is one title in history, however many of its episodes you've
// watched.
type ShowSummary struct {
	ID       string
	Name     string
	Type     string
	Source   string
	Year     string
	Base     string
	Episodes int   // watched or part-watched
	SeenAt   int64 // most recent activity
}

// HistoryShows groups history by title, most recently watched first.
//
// The episode list answers "what did I watch", which is the right view most
// of the time. It's the wrong one for clearing out a show you've finished —
// that means deleting thirty rows one at a time.
func HistoryShows() []ShowSummary {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	out := make([]ShowSummary, 0, len(Hist.data.Shows))
	for id, sh := range Hist.data.Shows {
		n := 0
		for _, e := range sh.Eps {
			if e.W || e.P > 0 {
				n++
			}
		}
		if n == 0 {
			continue
		}
		out = append(out, ShowSummary{
			ID: id, Name: sh.Name, Type: sh.Type, Source: sh.Source,
			Year: sh.Year, Base: sh.Base, Episodes: n, SeenAt: sh.SeenAt,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].SeenAt != out[j].SeenAt {
			return out[i].SeenAt > out[j].SeenAt
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ClearShow removes a title and everything under it.
func ClearShow(id string) {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	delete(Hist.data.Shows, id)

	kept := Hist.data.Recent[:0]
	for _, r := range Hist.data.Recent {
		if r.ShowID != id {
			kept = append(kept, r)
		}
	}
	clear(Hist.data.Recent[len(kept):]) // drop the tail's strings
	Hist.data.Recent = kept

	Hist.reindex()
	Hist.touch()
}

// showInRecent reports whether any history row still refers to a show.
func (h *HistoryStore) showInRecent(id string) bool {
	for _, r := range h.data.Recent {
		if r.ShowID == id {
			return true
		}
	}
	return false
}

// ClearHistoryEntry forgets one episode: the history row, the watched flag
// and the resume point.
//
// Removing only the row left the episode still ticked in the season list and
// still holding its position, so "delete from history" deleted the evidence
// and kept the effect.
//
// Matched by identity rather than list position: the screen's rows come from
// recentEntries, which skips any row whose show is missing, so its indices
// can drift from data.Recent and a delete would hit the neighbour.
func ClearHistoryEntry(e HistoryEntry) {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	idx := slices.IndexFunc(Hist.data.Recent, func(r RecentEntry) bool {
		return r.ShowID == e.ID && r.Season == e.Season && r.Episode == e.Episode
	})
	if idx < 0 {
		return
	}

	r := Hist.data.Recent[idx]
	Hist.data.Recent = append(Hist.data.Recent[:idx], Hist.data.Recent[idx+1:]...)

	if sh, ok := Hist.data.Shows[r.ShowID]; ok {
		delete(sh.Eps, EpKey(r.Season, r.Episode))

		// The show record goes only when nothing needs it: no episode state
		// and no other history row pointing at it.
		//
		// recentEntries drops any row whose show is missing, without
		// complaint — so removing the show one row too early would take
		// unrelated rows down with it and look like they were never there.
		if len(sh.Eps) == 0 && !Hist.showInRecent(r.ShowID) {
			delete(Hist.data.Shows, r.ShowID)
		}
	}
	if r.VideoID != "" {
		delete(Hist.byVideo, r.VideoID)
	}

	Hist.touch()
}

// ClearAllHistory drops everything, watched state included.
func ClearAllHistory() {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	Hist.data = HistoryFile{Version: HistoryVer, Shows: map[string]*ShowState{}}
	Hist.byVideo = map[string]EpRef{}
	Hist.touch()
}

// ── Continue watching ─────────────────────────────────────────────────────────

// ContinueItem is what the main menu offers you next.
type ContinueItem struct {
	Entry    HistoryEntry
	Position float64
	Duration float64

	NextUp    bool
	Airing    string // release date, when the next episode isn't out yet
	LastLabel string
	Index     int
	Total     int
}

func inProgress(e HistoryEntry) bool {
	if e.Watched {
		return false
	}
	return format.HasResumePoint(e.Position, e.Duration)
}

// ContinueTarget decides what "continue watching" should offer: resume the
// most recent thing if it isn't finished, otherwise the episode after it,
// otherwise anything else left part-watched.
func ContinueTarget() *ContinueItem {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	if inProgressValid {
		return inProgressCache
	}
	items := Hist.recentEntries().Items

	inProgressCache = nil
	inProgressValid = true
	if len(items) == 0 {
		return nil
	}

	resume := func(e HistoryEntry) *ContinueItem {
		return &ContinueItem{
			Entry: e, Position: e.Position, Duration: e.Duration,
			Index: e.Episode, Total: e.EpisodeTotal,
		}
	}

	top := items[0]
	if !top.Watched {
		inProgressCache = resume(top)
		return inProgressCache
	}

	// The next episode is derived from what's recorded for the show, rather
	// than stored on the entry: keeping next-up fields per episode meant
	// rewriting them for every episode of a bulk-marked season.
	// The next episode is whatever was recorded when this one played, not
	// Episode+1. An empty pointer means the queue had nothing after it — end
	// of the series, or a season we've no list for — so there's nothing to
	// offer rather than an episode that doesn't exist.
	if top.Type == "series" && top.NextVideoID != "" {
		if sh := Hist.data.Shows[top.ID]; sh != nil {
			nextKey := EpKey(top.NextSeason, top.NextEpisode)
			st := sh.Eps[nextKey]

			if st == nil || !st.W {
				e := HistoryEntry{
					Name: sh.Name, ID: top.ID, Type: sh.Type,
					Source: sh.Source, Year: sh.Year,
					Season: top.NextSeason, Episode: top.NextEpisode,
					VideoID: top.NextVideoID, EpTitle: top.NextTitle,
					EpisodeTotal: top.EpisodeTotal,
				}
				item := &ContinueItem{
					Entry:     e,
					NextUp:    true,
					LastLabel: format.Ep(top.Season, top.Episode, top.VideoID),
					Index:     top.NextEpisode,
					Total:     top.EpisodeTotal,
				}
				if !format.VideoAired(stremio.Video{Released: top.NextReleased}) {
					item.Airing = top.NextReleased
				}
				// Already started it — resume rather than announce it.
				if st != nil && st.P > 0 {
					item.NextUp = false
					item.Position, item.Duration = st.P, st.D
					item.Entry.Position, item.Entry.Duration = st.P, st.D
				}
				inProgressCache = item
				return inProgressCache
			}
		}
	}

	for i := range items {
		if inProgress(items[i]) {
			inProgressCache = resume(items[i])
			return inProgressCache
		}
	}
	return nil
}

// ContinueList returns the shows you're partway through, most recent first,
// one entry per show.
//
// ContinueTarget answers "what single thing next"; this answers "what am I in
// the middle of", which is a different question — you're usually three or four
// shows deep and the single most recent one isn't always the one you want.
func ContinueList(n int) []ContinueItem {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	items := Hist.recentEntries().Items
	seen := map[string]bool{}
	out := make([]ContinueItem, 0, n)

	for _, e := range items {
		if len(out) >= n {
			break
		}
		if e.ID == "" || seen[e.ID] {
			continue // one row per show, at whatever the latest point is
		}

		sh := Hist.data.Shows[e.ID]
		if sh == nil {
			continue
		}

		switch {
		case !e.Watched:
			seen[e.ID] = true
			out = append(out, ContinueItem{
				Entry: e, Position: e.Position, Duration: e.Duration,
				Index: e.Episode, Total: e.EpisodeTotal,
			})

		case e.Type == "series" && e.NextVideoID != "":
			// Same stored pointer the menu uses. A finished series records no
			// next episode, so it simply drops out of the list.
			seen[e.ID] = true

			next := sh.Eps[EpKey(e.NextSeason, e.NextEpisode)]
			if next != nil && next.W {
				continue // already seen it too
			}

			item := ContinueItem{
				Entry: HistoryEntry{
					Name: sh.Name, ID: e.ID, Type: sh.Type,
					Source: sh.Source, Year: sh.Year,
					Season: e.NextSeason, Episode: e.NextEpisode,
					VideoID: e.NextVideoID, EpTitle: e.NextTitle,
					EpisodeTotal: e.EpisodeTotal,
				},
				NextUp:    true,
				LastLabel: format.Ep(e.Season, e.Episode, e.VideoID),
				Index:     e.NextEpisode,
				Total:     e.EpisodeTotal,
			}
			if !format.VideoAired(stremio.Video{Released: e.NextReleased}) {
				item.Airing = e.NextReleased
			}
			if next != nil && next.P > 0 {
				item.NextUp = false
				item.Position, item.Duration = next.P, next.D
				item.Entry.Position, item.Entry.Duration = next.P, next.D
			}
			out = append(out, item)

		default:
			seen[e.ID] = true // finished film, nothing to continue
		}
	}
	return out
}

// RecentlyWatched returns the last few things you finished, newest first.
func RecentlyWatched(n int) []HistoryEntry {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	out := make([]HistoryEntry, 0, n)
	for _, e := range Hist.recentEntries().Items {
		if !e.Watched {
			continue
		}
		out = append(out, e)
		if len(out) >= n {
			break
		}
	}
	return out
}

// Stats summarises everything watched. Walks the in-memory store, so it costs
// nothing to recompute.
type Stats struct {
	Episodes int
	Films    int
	Shows    int
	Hours    float64
}

func HistoryStats() Stats {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	var st Stats
	for _, sh := range Hist.data.Shows {
		counted := false
		for k, e := range sh.Eps {
			if !e.W {
				continue
			}
			_, episode := parseEpKey(k)
			if episode > 0 {
				st.Episodes++
				counted = true
			} else {
				st.Films++
			}
			// Duration is only recorded for things actually played, so this
			// undercounts anything marked watched by hand rather than viewed.
			st.Hours += e.D / 3600
		}
		if counted {
			st.Shows++
		}
	}
	return st
}

// ── Rendering ─────────────────────────────────────────────────────────────────
