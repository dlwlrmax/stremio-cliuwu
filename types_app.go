package main

import (
	"sort"
	"stremio-cliuwu/internal/format"
	"stremio-cliuwu/internal/stremio"
	"time"
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

// configVersion is bumped whenever a new field needs a non-zero default.
// Without this, adding a bool to the struct silently gives every existing
// install `false`, because encoding/json just leaves absent fields alone.
const configVersion = 15

type AppConfig struct {
	Version          int    `json:"version"`
	MpvPath          string `json:"mpv_path"`
	PreferredQuality string `json:"preferred_quality"`
	SubtitleLang     string `json:"subtitle_lang"`
	AudioLang        string `json:"audio_lang"`
	AutoSubtitle     bool   `json:"auto_subtitle"`
	AutoSync         bool   `json:"auto_sync"`

	// Terms that hide a stream from the picker. Substring, case-insensitive,
	// matched against everything the addon says about it.
	Blocked        []string `json:"blocked,omitempty"`
	HistoryMax     int      `json:"history_max"`
	OmdbKey        string   `json:"omdb_key"`
	AutoNext       bool     `json:"auto_next"`
	AutoResume     bool     `json:"auto_resume"`
	CloseMpvOnExit bool     `json:"close_mpv_on_exit"`
	CachedFirst    bool     `json:"cached_first"`
	Accent         string   `json:"accent"`
	AutoInfo       bool     `json:"auto_info"`
	Posters        bool     `json:"posters"`
	PosterSize     string   `json:"poster_size"`

	// auto follows terminal detection, on and off force it either way.
	KittyMode string `json:"kitty_mode"`

	// Which poster metahub serves for the kitty path. Half-blocks always
	// take the small one, since they downscale to a few dozen cells and
	// cannot show the difference.
	PosterQuality string `json:"poster_quality"`

	// Off by default: an episode still is a frame from an episode you
	// haven't watched, which is a spoiler nobody asked for.
	EpisodeImages bool `json:"episode_images"`

	// Fetch the next page when the cursor reaches the load more row.
	AutoLoadMore bool `json:"auto_load_more"`

	DownloadDir     string `json:"download_dir"`
	DownloadFolders bool   `json:"download_folders"`
	MoviePattern    string `json:"movie_pattern"`
	EpisodePattern  string `json:"episode_pattern"`
	DateFormat      string `json:"date_format"`

	// What mpv shows as the title. "default" leaves mpv to work it out from
	// the url, "formatted" uses the built-in forms, anything else is a
	// pattern using the same placeholders as the download filenames.
	MpvTitle string `json:"mpv_title"`

	// Stream list layout, kept between sessions. Hide rather than show so
	// the details pane is on for a config written before it existed.
	HideStreamDetails bool   `json:"hide_stream_details"`
	StreamAudio       bool   `json:"stream_audio"`
	StreamSort        string `json:"stream_sort"` // "", "size" or "res"
}

// SetDefaults fills in anything missing. Returns true if it changed something,
// so the caller can persist the upgrade once rather than on every load.
func (c *AppConfig) SetDefaults() bool {
	changed := false

	if c.Version < 1 {
		// Pre-versioned config (or a fresh one): opt into the behaviour that
		// used to be implicit.
		c.AutoNext = true
		c.AutoResume = true
		c.CloseMpvOnExit = true
		c.Version = 1
		changed = true
	}

	if c.Version < 2 {
		c.CachedFirst = true
		changed = true
	}

	if c.Version < 3 {
		c.Accent = "pink"
		changed = true
	}

	if c.Version < 4 {
		c.AutoInfo = true
		changed = true
	}

	if c.Version < 5 {
		c.Posters = true
		changed = true
	}

	if c.Version < 6 {
		c.Posters = false // opt-in: half-block art is rough at this size
		changed = true
	}

	if c.Version < 7 || c.PosterSize == "" {
		c.PosterSize = "medium"
		changed = true
	}

	// Posters on at xl by default: they were opt-in because half-block art
	// was rough, and on a kitty terminal it isn't art any more.
	if c.Version < 13 {
		c.Posters = true
		c.PosterSize = "xl"
		changed = true
	}

	if c.KittyMode == "" {
		c.KittyMode = "auto"
		changed = true
	}

	if c.PosterQuality == "" {
		c.PosterQuality = "large"
		changed = true
	}

	if c.Version < 8 {
		c.DownloadFolders = true
		changed = true
	}

	if c.MpvTitle == "" {
		c.MpvTitle = "default"
		changed = true
	}

	if c.MoviePattern == "" {
		c.MoviePattern = DefaultMoviePattern
		changed = true
	}
	if c.EpisodePattern == "" {
		c.EpisodePattern = DefaultEpisodePattern
		changed = true
	}
	if c.DateFormat == "" {
		c.DateFormat = "dmy"
		changed = true
	}

	// A default rather than blank: an unset language leaves the subtitle
	// picker opening on "all", which is the state every edge case shows up
	// in. Clear it deliberately and it stays clear.
	if c.Version < 11 && c.SubtitleLang == "" {
		// Spelled out rather than just "eng": all three resolve to English
		// anyway, but seeing the list in the setting is what tells you it
		// takes a list at all.
		c.SubtitleLang = "eng, en, English"
		changed = true
	}

	if c.Version < 13 {
		c.AutoSubtitle = true // opt out, not in: most people want subtitles
		changed = true
	}

	if c.Version < 14 {
		c.AutoSync = true // opt out, not in: a signed-in account keeps itself in step
		changed = true
	}

	// Audio is ranked purely by this preference order (see Player.maybeAutoAudio);
	// it is also handed to mpv as --alang.
	if c.Version < 15 && c.AudioLang == "" {
		c.AudioLang = "jpn,chi,eng"
		changed = true
	}

	if c.HistoryMax <= 0 {
		c.HistoryMax = 300
		changed = true
	}
	if c.OmdbKey == "" {
		c.OmdbKey = "trilogy"
		changed = true
	}

	c.Version = configVersion
	return changed
}

// ── Favourites ────────────────────────────────────────────────────────────────

type Favourite struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	Source string `json:"source"`
	Year   string `json:"year"`
	Season int    `json:"season"` // 0 = whole show
	Added  string `json:"added"`

	// Same reason as showState.Base: a favourited library entry is opened
	// by asking its addon for the file list, and nothing else records which
	// addon that was.
	Base string `json:"base,omitempty"`
}

type FavouriteList struct {
	Items []Favourite `json:"items"`
}

// ── History ───────────────────────────────────────────────────────────────────

type HistoryEntry struct {
	Name     string  `json:"name"`
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Source   string  `json:"source"`
	Year     string  `json:"year"`
	Season   int     `json:"season,omitempty"`
	Episode  int     `json:"episode,omitempty"`
	VideoID  string  `json:"video_id,omitempty"`
	EpTitle  string  `json:"ep_title,omitempty"`
	Position float64 `json:"position,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Watched  bool    `json:"watched"`

	// The addon that served this, for library entries. Their files live
	// behind a meta lookup rather than a stream search, so resuming one
	// without knowing which addon to ask returns nothing at all.
	Base      string    `json:"base,omitempty"`
	WatchedAt time.Time `json:"watched_at"`

	// Recorded when playback starts so the menu can offer the next episode
	// without a network round trip. Working it out at render time would mean
	// fetching the season's episode list every time the menu draws.
	EpisodeTotal int    `json:"episode_total,omitempty"`
	NextVideoID  string `json:"next_video_id,omitempty"`
	NextSeason   int    `json:"next_season,omitempty"`
	NextEpisode  int    `json:"next_episode,omitempty"`
	NextTitle    string `json:"next_title,omitempty"`
	NextReleased string `json:"next_released,omitempty"`
}

type HistoryList struct {
	Items []HistoryEntry `json:"items"`
}
