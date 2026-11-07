package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"stremio-cliuwu/internal/bitfield"
	"stremio-cliuwu/internal/jsonio"
	"stremio-cliuwu/internal/paths"
	"stremio-cliuwu/internal/stremio"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Stremio account login and a two-way library sync.
//
// Sign in, read what the server holds, union it into local history, and send
// back whatever this device watched more recently. Tombstones are still out of
// scope — a title you removed on strem.io stays put locally, and vice versa.
//
// Everything talks to api.strem.io, which wraps every response in
// {"result":...} | {"error":{"message","code"}}. The authKey that comes back
// is a credential and is treated like one: it lives in its own 0600 file and
// is never rendered anywhere.

const urlStremioAPI = "https://api.strem.io/api"

// ── Wire types ────────────────────────────────────────────────────────────────

type stremioErr struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type stremioEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *stremioErr     `json:"error"`
}

// libraryItem is the subset of Stremio's LibraryItem we handle. The per-episode
// watched bitfield is decoded via bitfield.Decode; behaviorHints is still
// ignored.
type libraryItem struct {
	ID      string        `json:"_id"`
	Name    string        `json:"name"`
	Type    string        `json:"type"` // "movie" | "series"
	Removed bool          `json:"removed"`
	MTime   string        `json:"_mtime"` // RFC3339
	Poster  string        `json:"poster,omitempty"`
	State   libraryItemSt `json:"state"`
}

type libraryItemSt struct {
	TimeWatched    float64 `json:"timeWatched"`           // ms total watched
	TimeOffset     float64 `json:"timeOffset"`            // ms resume point
	TimesWatched   int     `json:"timesWatched"`          // >0 means finished at least once
	FlaggedWatched int     `json:"flaggedWatched"`        // manual tick
	Duration       float64 `json:"duration"`              // ms
	VideoID        string  `json:"video_id"`              // "id:s:e" for series; the server drops any other spelling
	LastWatched    string  `json:"lastWatched,omitempty"` // RFC3339
	Watched        string  `json:"watched,omitempty"`     // per-episode bitfield, series only
}

// ── Persistence ───────────────────────────────────────────────────────────────

// accountInfo is what account.json holds. authKey is the secret; email and
// userID are kept only so the settings screen can show who's signed in.
type accountInfo struct {
	AuthKey string `json:"authKey"`
	Email   string `json:"email"`
	UserID  string `json:"userID"`
}

func LoadAccount() accountInfo {
	var a accountInfo
	if err := jsonio.Read(paths.AccountFile(), &a); err != nil {
		return accountInfo{}
	}
	return a
}

// SaveAccount writes 0600 via jsonio.Write, the same treatment addons.json gets
// because it also carries a token.
func SaveAccount(a accountInfo) error { return jsonio.Write(paths.AccountFile(), a) }

func ClearAccount() { _ = os.Remove(paths.AccountFile()) }

// ── HTTP ──────────────────────────────────────────────────────────────────────

// stremioPost sends a JSON body and unwraps the envelope into out. It reuses
// stremio.HTTPClient, whose 15s timeout covers the whole request — a login that hangs
// otherwise locks the UI's sync command forever.
func stremioPost(endpoint string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", urlStremioAPI+"/"+endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	r, err := stremio.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()

	var env stremioEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		return fmt.Errorf("stremio: %w", err)
	}
	if env.Error != nil {
		if env.Error.Message != "" {
			return fmt.Errorf("%s", env.Error.Message)
		}
		return fmt.Errorf("stremio api error %d", env.Error.Code)
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("stremio: %w", err)
		}
	}
	return nil
}

// ── API ───────────────────────────────────────────────────────────────────────

// StremioLogin exchanges credentials for an authKey. The password is used and
// discarded; only the returned key is persisted.
func StremioLogin(email, password string) (accountInfo, error) {
	var res struct {
		AuthKey string `json:"authKey"`
		User    struct {
			ID    string `json:"_id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	err := stremioPost("login", map[string]any{
		"type":     "Login",
		"email":    email,
		"password": password,
		"facebook": false,
	}, &res)
	if err != nil {
		return accountInfo{}, err
	}
	if res.AuthKey == "" {
		return accountInfo{}, fmt.Errorf("stremio: login returned no auth key")
	}
	a := accountInfo{AuthKey: res.AuthKey, Email: email, UserID: res.User.ID}
	if res.User.Email != "" {
		a.Email = res.User.Email
	}
	return a, nil
}

func StremioLogout(authKey string) error {
	return stremioPost("logout", map[string]any{"type": "logout", "authKey": authKey}, nil)
}

// datastoreMetaEntry is one [id, mtimeMs] pair from datastoreMeta.
type datastoreMetaEntry struct {
	ID      string
	MtimeMs int64
}

// DatastoreMeta lists what the server holds for a collection. Not on the sync
// path yet (the first pull asks for everything), but it's what a future
// incremental sync would diff against.
func DatastoreMeta(authKey, collection string) ([]datastoreMetaEntry, error) {
	var raw [][]json.RawMessage
	err := stremioPost("datastoreMeta", map[string]any{
		"authKey":    authKey,
		"collection": collection,
	}, &raw)
	if err != nil {
		return nil, err
	}

	out := make([]datastoreMetaEntry, 0, len(raw))
	for _, pair := range raw {
		if len(pair) < 2 {
			continue
		}
		var id string
		var mtime float64
		_ = json.Unmarshal(pair[0], &id)
		_ = json.Unmarshal(pair[1], &mtime)
		out = append(out, datastoreMetaEntry{ID: id, MtimeMs: int64(mtime)})
	}
	return out, nil
}

// DatastoreGet fetches full items by id. Pass all=true to take the collection
// whole — that's what the first sync does.
func DatastoreGet(authKey, collection string, ids []string, all bool) ([]libraryItem, error) {
	if ids == nil {
		ids = []string{}
	}
	var out []libraryItem
	err := stremioPost("datastoreGet", map[string]any{
		"authKey":    authKey,
		"collection": collection,
		"ids":        ids,
		"all":        all,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// datastorePut writes local items back to the collection. The API answers
// {"success":true}; stremioPost unwraps that wherever it sits and surfaces an
// error envelope as a returned error.
func datastorePut(authKey string, items []libraryItem) error {
	return stremioPost("datastorePut", map[string]any{
		"authKey":    authKey,
		"collection": "libraryItem",
		"changes":    items,
	}, nil)
}

// pullLibrary fetches the whole remote library and merges it in, returning the
// number of items applied and what the server sent.
func pullLibrary(authKey string) (int, []libraryItem, error) {
	items, err := DatastoreGet(authKey, "libraryItem", nil, true)
	if err != nil {
		return 0, nil, err
	}
	return MergeLibrary(items), items, nil
}

// selectPush picks the local titles worth sending. A title goes up when the
// server lacks it or holds an older copy. It also goes up when the server's
// copy is no newer than ours but carries no episode pointer (blank): earlier
// builds sent the pointer under a name the server drops, and a device reading
// such a copy can't tell which episode the progress is for. A blank copy that
// is newer than ours stays untouched — it is another device's progress, and
// overwriting it would lose that.
func selectPush(local []LocalLibraryEntry, remote map[string]int64, blank map[string]bool) []libraryItem {
	var changes []libraryItem
	for _, e := range local {
		if rm, ok := remote[e.ID]; ok && rm >= e.MTime && !(blank[e.ID] && rm <= e.MTime) {
			continue // the server's copy is at least as new, and usable
		}
		changes = append(changes, e.toLibraryItem())
	}
	return changes
}

// syncAccount does one two-way pass: read the remote metadata, pull what's
// newer there, then push what's newer here. The metadata only drives the push
// decision — pulling everything is cheap and MergeLibrary already keeps the
// newer side, so nothing needs per-id fetches.
func syncAccount(authKey string) (pulled, pushed int, err error) {
	meta, err := DatastoreMeta(authKey, "libraryItem")
	if err != nil {
		return 0, 0, err
	}
	remote := make(map[string]int64, len(meta))
	for _, m := range meta {
		remote[m.ID] = m.MtimeMs / 1000
	}

	pulled, items, err := pullLibrary(authKey)
	if err != nil {
		return pulled, 0, err
	}
	blank := map[string]bool{}
	for _, it := range items {
		if !it.Removed && it.State.VideoID == "" {
			blank[it.ID] = true
		}
	}

	changes := selectPush(LocalLibrary(), remote, blank)
	if len(changes) == 0 {
		return pulled, 0, nil
	}
	if err := datastorePut(authKey, changes); err != nil {
		return pulled, 0, err
	}
	return pulled, len(changes), nil
}

// toLibraryItem mirrors a local title into Stremio's shape. A series reports
// the episode most recently touched, addressed "<showID>:<season>:<episode>";
// a film reports the show id itself.
func (e LocalLibraryEntry) toLibraryItem() libraryItem {
	typ, videoID := "series", ""
	if e.Episode == 0 && e.Season == 0 {
		typ, videoID = "movie", e.ID
	} else {
		videoID = StremioVideoID(e.ID, e.Season, e.Episode)
	}

	mtime := time.Unix(e.MTime, 0).UTC().Format(time.RFC3339)
	st := libraryItemSt{
		TimeOffset:  e.Position * 1000,
		Duration:    e.Duration * 1000,
		VideoID:     videoID,
		LastWatched: mtime,
		Watched:     e.Bitfield,
	}
	if e.Watched {
		st.TimesWatched = 1
		st.FlaggedWatched = 1
	}
	return libraryItem{ID: e.ID, Name: e.Name, Type: typ, MTime: mtime, Poster: e.Poster, State: st}
}

// ── Merge ─────────────────────────────────────────────────────────────────────

// MergeLibrary unions remote items into local history and returns how many
// were written.
//
// Recency decides conflicts: an episode whose local M is at least as new as
// the remote _mtime is left alone, so pulling never rewinds progress you made
// here. Removed items are skipped rather than deleted — tombstones are out of
// scope for this pass.
//
// Unfinished remote items also get a recent row, so a pulled in-progress title
// shows up under continue watching without a play here first. Fully-watched
// items get no row, so the rails aren't flooded with things you've finished.
func MergeLibrary(items []libraryItem) int {
	Hist.mu.Lock()
	defer Hist.mu.Unlock()
	Hist.load()

	applied := 0
	for _, it := range items {
		if it.Removed || it.ID == "" || it.Name == "" {
			continue
		}

		key := "0:0"
		season, episode := 0, 0
		if it.Type == "series" {
			s, e, ok := EpisodeOfVideoID(it.ID, it.State.VideoID)
			if !ok {
				continue // no usable episode pointer — nothing to record
			}
			season, episode = s, e
			key = EpKey(s, e)
		}

		mtime := parseStremioTime(it.MTime)
		if local := Hist.ep(it.ID, key); local != nil && epMTime(local) >= mtime {
			// Local progress wins this episode, but a poster is show-level
			// state, not progress: adopt the server's when we have none, so
			// the next push can't send an empty poster over it.
			if it.Poster != "" {
				if sh := Hist.data.Shows[it.ID]; sh != nil && sh.Poster == "" {
					sh.Poster = it.Poster
				}
			}
			continue // local is newer; remote loses the tie
		}

		sh := Hist.show(it.ID, stremio.Meta{Name: it.Name, Type: it.Type, Source: "stremio"})
		if sh.Poster == "" {
			sh.Poster = it.Poster
		}

		// The bitfield carries episodes the coarse videoId pointer doesn't.
		// Apply it before the single-episode fields below so the anchor's real
		// position and duration still win over "watched implies finished".
		if it.Type == "series" && it.State.Watched != "" {
			Hist.applyWatchedBitfield(sh, it.ID, it.State.Watched, mtime)
		}

		st := sh.Eps[key]
		if st == nil {
			st = &EpRecord{}
			sh.Eps[key] = st
		}
		st.W = it.State.TimesWatched > 0 || it.State.FlaggedWatched > 0
		st.P = it.State.TimeOffset / 1000
		st.D = it.State.Duration / 1000
		st.T = mtime
		st.M = mtime
		sh.SeenAt = max(sh.SeenAt, mtime)

		if !st.W && it.State.TimeOffset > 0 {
			Hist.ensureRecent(HistoryEntry{
				Name: it.Name, ID: it.ID, Type: it.Type, Source: "stremio",
				Season: season, Episode: episode, VideoID: it.State.VideoID,
				Position: st.P, Duration: st.D, WatchedAt: time.Unix(mtime, 0),
			}, mtime)
		}
		applied++
	}

	Hist.reindex()
	Hist.touch() // dirty + invalidateInProgress + debounced flush
	return applied
}

// applyWatchedBitfield marks every episode a remote bitfield reports watched.
// Index i addresses the i-th episode in the same order the encoder uses; bits
// past the episodes we know are ignored. The anchor — the most recently
// watched episode — is marked directly, so it lands even when it isn't in the
// local ordering yet. Callers hold the lock.
func (h *HistoryStore) applyWatchedBitfield(sh *ShowState, showID, bf string, mtime int64) {
	anchorID, length, bits, err := bitfield.Decode(bf)
	if err != nil {
		return
	}

	eps := orderedEpisodes(showID, sh)
	mark := func(season, episode int) {
		if episode <= 0 {
			return
		}
		key := EpKey(season, episode)
		st := sh.Eps[key]
		if st == nil {
			st = &EpRecord{}
			sh.Eps[key] = st
		}
		st.W = true
		st.T = mtime
		st.M = mtime
		if st.P == 0 && st.D > 0 {
			st.P = st.D
		}
	}

	// Bits are placed relative to the anchor (see bitfield.Aligned). With the
	// anchor missing from our list there is nothing to align to, and
	// stremio-core treats that as a blank field too; only the anchor itself,
	// marked below, is trusted.
	if ai := AnchorIndex(showID, anchorID, eps); ai >= 0 {
		for _, i := range bitfield.Aligned(length, ai, len(eps), bits) {
			mark(eps[i][0], eps[i][1])
		}
	}
	if s, e, ok := EpisodeOfVideoID(showID, anchorID); ok {
		mark(s, e)
	}
}

// AnchorIndex finds the anchor video in the ordered episode list. Matched on
// season and episode rather than the id string, which may be spelled
// differently by the other side. -1 when it isn't there, or when the id isn't
// one of this show's episode ids.
func AnchorIndex(showID, anchorID string, eps [][2]int) int {
	if !strings.HasPrefix(anchorID, showID+":") {
		return -1
	}
	s, e, ok := EpisodeOfVideoID(showID, anchorID)
	if !ok {
		return -1
	}
	for i, oe := range eps {
		if oe[0] == s && oe[1] == e {
			return i
		}
	}
	return -1
}

// StremioVideoID is the id Stremio gives an episode: "<showID>:<season>:<episode>",
// except for Kitsu titles, whose single season is left out ("kitsu:1234:5").
func StremioVideoID(showID string, season, episode int) string {
	if strings.HasPrefix(showID, "kitsu:") && season == 1 {
		return fmt.Sprintf("%s:%d", showID, episode)
	}
	return fmt.Sprintf("%s:%d:%d", showID, season, episode)
}

// EpisodeOfVideoID is the reverse of StremioVideoID. Knowing the show id lets
// it tell "kitsu:1234:5" (episode 5) from "tt1:2:5" (season 2, episode 5); the
// bare last-two-segments reading in videoEpisode takes the Kitsu id for a
// season. An id that doesn't start with the show id falls back to that.
func EpisodeOfVideoID(showID, videoID string) (int, int, bool) {
	rest, ok := strings.CutPrefix(videoID, showID+":")
	if !ok || showID == "" {
		return videoEpisode(videoID)
	}
	parts := strings.Split(rest, ":")
	switch len(parts) {
	case 2:
		s, err1 := strconv.Atoi(parts[0])
		e, err2 := strconv.Atoi(parts[1])
		if err1 == nil && err2 == nil && s >= 0 && e >= 0 {
			return s, e, true
		}
	case 1:
		if strings.HasPrefix(showID, "kitsu:") {
			if e, err := strconv.Atoi(parts[0]); err == nil && e >= 0 {
				return 1, e, true
			}
		}
	}
	return 0, 0, false
}

// videoEpisode pulls (season, episode) out of a Stremio series videoId of the
// form "<showID>:<season>:<episode>" — the last two colon segments. Titles
// whose id itself contains a colon (some anime) are not handled and are
// skipped rather than guessed at.
func videoEpisode(videoID string) (int, int, bool) {
	i := strings.LastIndexByte(videoID, ':')
	if i <= 0 {
		return 0, 0, false
	}
	j := strings.LastIndexByte(videoID[:i], ':')
	if j < 0 {
		return 0, 0, false
	}
	s, err1 := strconv.Atoi(videoID[j+1 : i])
	e, err2 := strconv.Atoi(videoID[i+1:])
	if err1 != nil || err2 != nil || s < 0 || e < 0 {
		return 0, 0, false
	}
	return s, e, true
}

func parseStremioTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// ── UI bridge ─────────────────────────────────────────────────────────────────

// StremioSyncDoneMsg carries a finished login or sync back to the settings
// screen. It's a plain tea.Msg so the network work can run in a command
// goroutine without blocking Update.
type StremioSyncDoneMsg struct {
	Pulled   int
	Pushed   int
	Err      error
	SignedIn bool
	Email    string
}

// HistorySyncedMsg says a background pull changed local history, so the UI
// can drop its caches and rebuild the continue-watching row.
type HistorySyncedMsg struct{}

// syncBusy serialises the network syncs. A file ending can fire a push while
// the boot pull or a manual sync is still in flight; overlapping passes would
// both merge the same remote library and race each other's puts.
var syncBusy atomic.Bool

// AutoSyncPush pushes this device's history after a file ends. Callers check
// the AutoSync setting first. It is silent on failure — the next manual sync
// reports the counts — and skips if another sync owns the flag.
func AutoSyncPush() {
	a := LoadAccount()
	if a.AuthKey == "" || !syncBusy.CompareAndSwap(false, true) {
		return
	}
	defer syncBusy.Store(false)
	_, _, _ = syncAccount(a.AuthKey)
}

// AutoSyncPull runs the boot sync once the app is up and tells the UI when a
// pull landed so continue watching picks it up. Silent on failure.
func AutoSyncPull(prog *tea.Program) {
	a := LoadAccount()
	if a.AuthKey == "" || !syncBusy.CompareAndSwap(false, true) {
		return
	}
	_, _, err := syncAccount(a.AuthKey)
	syncBusy.Store(false)
	if err == nil {
		prog.Send(HistorySyncedMsg{})
	}
}

// StremioLoginCmd logs in, persists the account, then immediately syncs so
// signing in is one action rather than two.
func StremioLoginCmd(email, password string) tea.Cmd {
	return func() tea.Msg {
		a, err := StremioLogin(email, password)
		if err != nil {
			return StremioSyncDoneMsg{Err: err}
		}
		if err := SaveAccount(a); err != nil {
			return StremioSyncDoneMsg{Err: err}
		}
		if !syncBusy.CompareAndSwap(false, true) {
			return StremioSyncDoneMsg{SignedIn: true, Email: a.Email}
		}
		pulled, pushed, err := syncAccount(a.AuthKey)
		syncBusy.Store(false)
		return StremioSyncDoneMsg{Pulled: pulled, Pushed: pushed, Err: err, SignedIn: true, Email: a.Email}
	}
}

// StremioSyncCmd runs the two-way sync for an already signed-in account.
func StremioSyncCmd() tea.Cmd {
	return func() tea.Msg {
		a := LoadAccount()
		if a.AuthKey == "" {
			return StremioSyncDoneMsg{Err: fmt.Errorf("not signed in")}
		}
		if !syncBusy.CompareAndSwap(false, true) {
			return StremioSyncDoneMsg{Err: fmt.Errorf("sync already running")}
		}
		pulled, pushed, err := syncAccount(a.AuthKey)
		syncBusy.Store(false)
		return StremioSyncDoneMsg{Pulled: pulled, Pushed: pushed, Err: err}
	}
}
