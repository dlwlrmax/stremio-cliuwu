package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The old client used `loadfile <url> append-play`, which meant mpv owned a
// playlist and we had to reverse-engineer "what's playing" by asking mpv for
// its current path and matching it against a URL→videoID map. That map is gone.
//
// With `replace` there is exactly one file loaded at any moment, so the
// currently playing item is just p.now. Everything downstream — position
// saving, watched marking, the status bar — reads one field instead of doing
// prefix matching against a map that could go stale.
//
// The one thing we lose is mpv's own playlist advance. Rather than silently
// picking a stream for the next episode, the player emits EpisodeEndedMsg and
// the TUI opens that episode's stream list — see EpisodeEndedMsg below.

func socketPath() string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\stremio-cliuwu`
	}
	return "/tmp/stremio-cliuwu.sock"
}

var errNoMpv = errors.New("mpv is not running")

// ── Messages into the Bubble Tea program ──────────────────────────────────────

type PlayerStateMsg struct{ State PlayerState }
type PlayerNoticeMsg struct{ Text string }
type PlayerErrMsg struct{ Err error }

// EpisodeEndedMsg fires when a queued episode plays to its end. The player
// deliberately does not pick the next stream itself: it can't tell a cached
// debrid result from one that needs downloading, and a blind pick that fails
// leaves you with an error and no list to retry from. The TUI opens the next
// episode's stream picker instead.
type EpisodeEndedMsg struct{ Prev PlayRequest }

// PrefetchNextMsg fires once the current episode is far enough through that
// the next one is a safe bet. Waiting for EOF meant the stream list only
// started loading after playback had already stopped; this way the next
// episode's streams are resolved and on screen while you're still watching.
type PrefetchNextMsg struct{ Prev PlayRequest }

// How far before the end to look ahead is worked out from the runtime — see
// prefetchLead. A flat fraction fired 37 minutes early on a long film.

type PlayerState struct {
	Alive     bool
	Loading   bool
	Buffering bool
	Paused    bool
	Label     string // our label, e.g. "Frieren · S01E04"
	NextLabel string // queued to follow this one, empty if nothing is
	Title     string // mpv's media-title
	VideoID   string
	Pos       float64
	Duration  float64
	QueuePos  int
	QueueLen  int
}

func (s PlayerState) Frac() float64 {
	if s.Duration <= 0 {
		return 0
	}
	return s.Pos / s.Duration
}

func (s PlayerState) Percent() float64 { return s.Frac() * 100 }

// ── Play request ──────────────────────────────────────────────────────────────

type PlayRequest struct {
	VideoID   string
	MediaType string
	Label     string
	URL       string
	Resume    float64
	Entry     HistoryEntry // written to history when playback starts
	Queue     *EpQueue     // non-nil for series, drives autoplay
	Addons    []Addon      // needed to resolve the next episode

	// Carried from the chosen stream so the subtitle picker can ask for
	// this exact file rather than the title in general.
	VideoHash string
	VideoSize int64
	Filename  string
	Subs      []Subtitle // shipped with the stream itself
}

// ── Player ────────────────────────────────────────────────────────────────────

type Player struct {
	cfg  AppConfig
	prog *tea.Program

	mu      sync.Mutex
	conn    net.Conn
	pending map[uint32]chan map[string]any
	now     *PlayRequest
	state   PlayerState
	seq     uint32

	wmu sync.Mutex // serialises writes to conn

	queued     *PlayRequest // plays automatically when the current file ends
	replacing  bool         // suppress the end-file/stop that a replace generates
	prefetched bool         // next episode already surfaced for this request
	autoSubbed bool         // subtitle autoload already attempted for this request
	playGen    uint64       // bumped on every play(); stale subtitle fetches drop out
	seekTo     float64      // pending resume seek, applied on file-loaded
	lastSave   time.Time    // throttles history writes
	lastEmit   int          // last whole second pushed to the UI
}

func NewPlayer(cfg AppConfig) *Player {
	return &Player{cfg: cfg, pending: map[uint32]chan map[string]any{}}
}

func (p *Player) Attach(prog *tea.Program) { p.prog = prog }
func (p *Player) SetConfig(cfg AppConfig)  { p.mu.Lock(); p.cfg = cfg; p.mu.Unlock() }

func (p *Player) emit(msg tea.Msg) {
	if p.prog != nil {
		p.prog.Send(msg)
	}
}

func (p *Player) State() PlayerState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Queue holds a stream to play when the current one ends. Queueing again
// replaces what was there — you're changing your mind about what's next, not
// building a list.
//
// Neither this nor Unqueue emits: they're called from a screen's Update, and
// Program.Send writes to an unbuffered channel drained by the very event loop
// that is running Update — so emitting from there blocks waiting for itself.
// Callers return playerStateCmd() instead, letting the runtime deliver it.
func (p *Player) Queue(req PlayRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.queued = &req
	p.state.NextLabel = req.Label
}

func (p *Player) Unqueue() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.queued = nil
	p.state.NextLabel = ""
}

// AddSubtitle loads a subtitle track into the running file and selects it.
//
// mpv fetches the URL itself, so nothing is downloaded here — which also
// means a dead link fails inside mpv rather than anywhere we can report.
func (p *Player) AddSubtitle(url, title, lang string) tea.Cmd {
	return func() tea.Msg {
		// mpv fetches this itself, and handing it anything that isn't http(s)
		// is at best a local file it shouldn't be reading on our say-so. The
		// autoloader treats the error like a dead link and moves on.
		u, perr := neturl.Parse(url)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return SubtitleAddedMsg{URL: url, Title: title, Err: errors.New("subtitle url is not http(s)")}
		}
		if title == "" {
			title = "subtitle"
		}

		// Generous: mpv downloads the file before it answers, and some
		// subtitle hosts are slow enough that a short wait is a false
		// negative rather than a failure.
		_, err := p.commandWait(30*time.Second, "sub-add", url, "select", title, lang)
		return SubtitleAddedMsg{URL: url, Title: title, Err: err}
	}
}

// SubtitleAddedMsg reports whether mpv actually took the track, so the picker
// can mark the one that's really playing rather than the one you last pressed.
type SubtitleAddedMsg struct {
	URL   string
	Title string
	Err   error
}

// Queued returns what's lined up, if anything.
func (p *Player) Queued() *PlayRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queued
}

func (p *Player) Now() *PlayRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.now
}

// ── Connection ────────────────────────────────────────────────────────────────

func (p *Player) connect() error {
	p.mu.Lock()
	live := p.conn != nil
	p.mu.Unlock()
	if live {
		return nil
	}

	c := ipcDial()
	if c == nil {
		if err := p.spawn(); err != nil {
			return err
		}
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if c = ipcDial(); c != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if c == nil {
			return errors.New("mpv started but its IPC socket never appeared")
		}
	}

	p.mu.Lock()
	p.conn = c
	p.state.Alive = true
	p.mu.Unlock()

	go p.readLoop(c)
	p.observe()
	return nil
}

func (p *Player) spawn() error {
	if runtime.GOOS != "windows" {
		os.Remove(socketPath())
	}
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()

	bin := cfg.MpvPath
	if bin == "" {
		bin = "mpv"
	}

	args := []string{
		"--idle=yes",
		"--force-window=yes",
		"--keep-open=no",
		// --no-terminal is not optional here: we run inside the alt screen,
		// and anything mpv prints to our tty shreds the TUI.
		"--no-terminal",
		"--input-ipc-server=" + socketPath(),
	}
	if cfg.SubtitleLang != "" {
		args = append(args, "--slang="+cfg.SubtitleLang)
	}
	if cfg.AudioLang != "" {
		args = append(args, "--alang="+cfg.AudioLang)
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start mpv (%s): %w", bin, err)
	}
	go cmd.Wait() // don't leave a zombie
	return nil
}

func (p *Player) readLoop(c net.Conn) {
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var m map[string]any
			if json.Unmarshal(line, &m) == nil {
				p.dispatch(m)
			}
		}
		if err != nil {
			break
		}
	}
	p.gone()
}

func (p *Player) gone() {
	p.mu.Lock()
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
	for id, ch := range p.pending {
		close(ch)
		delete(p.pending, id)
	}
	now := p.now
	st := p.state
	p.now = nil
	p.state = PlayerState{}

	// Nothing left to follow. Left set, it would suppress the next-episode
	// picker for the rest of the session — Queued() being non-nil is what
	// tells the app a choice has already been made.
	p.queued = nil
	p.mu.Unlock()

	if now != nil && st.Duration > 0 && st.Pos > 0 {
		UpdatePosition(now.VideoID, st.Pos, st.Duration)
	}
	p.emit(PlayerStateMsg{})
}

// sendAsync runs IPC work off the read loop.
//
// dispatch is called synchronously from readLoop, and command() waits for a
// reply that only readLoop can deliver — so calling it from an event handler
// deadlocks until the timeout fires. The command still reaches mpv, which is
// why this looked like it worked; what it actually did was stall every
// property update for four seconds after each file load.
func (p *Player) sendAsync(fn func()) { go fn() }

// command sends an mpv IPC command and waits for the matching response.
// command sends an mpv IPC command and waits the default time for a reply.
func (p *Player) command(args ...any) (map[string]any, error) {
	return p.commandWait(4*time.Second, args...)
}

// commandWait is command with its own deadline.
//
// Most commands are instant — a seek, a property set. sub-add is not: mpv
// fetches the file before replying, and a slow subtitle host takes longer
// than any sane default. Reporting failure at four seconds meant saying a
// subtitle hadn't loaded while mpv was still loading it.
func (p *Player) commandWait(wait time.Duration, args ...any) (map[string]any, error) {
	p.mu.Lock()
	c := p.conn
	if c == nil {
		p.mu.Unlock()
		return nil, errNoMpv
	}
	id := atomic.AddUint32(&p.seq, 1)
	ch := make(chan map[string]any, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	payload, _ := json.Marshal(map[string]any{"command": args, "request_id": id})
	payload = append(payload, '\n')

	p.wmu.Lock()
	_, err := c.Write(payload)
	p.wmu.Unlock()
	if err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, errNoMpv
		}
		if e, _ := resp["error"].(string); e != "success" {
			return resp, fmt.Errorf("mpv: %s", e)
		}
		return resp, nil
	case <-time.After(wait):
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, errors.New("mpv did not respond")
	}
}

func (p *Player) observe() {
	props := []string{"time-pos", "duration", "pause", "media-title", "paused-for-cache"}
	for i, prop := range props {
		p.command("observe_property", i+1, prop)
	}
}

// ── Event dispatch ────────────────────────────────────────────────────────────

func (p *Player) dispatch(m map[string]any) {
	if rid, ok := m["request_id"].(float64); ok {
		p.mu.Lock()
		ch := p.pending[uint32(rid)]
		delete(p.pending, uint32(rid))
		p.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		return
	}

	switch ev, _ := m["event"].(string); ev {
	case "property-change":
		name, _ := m["name"].(string)
		p.onProperty(name, m["data"])
	case "file-loaded":
		p.onFileLoaded()
	case "end-file":
		reason, _ := m["reason"].(string)
		p.onEndFile(reason)
	case "shutdown":
		p.gone()
	}
}

func (p *Player) onProperty(name string, data any) {
	p.mu.Lock()
	switch name {
	case "time-pos":
		if f, ok := data.(float64); ok {
			p.state.Pos = f
		}
	case "duration":
		if f, ok := data.(float64); ok {
			p.state.Duration = f
		}
	case "pause":
		p.state.Paused, _ = data.(bool)
	case "paused-for-cache":
		p.state.Buffering, _ = data.(bool)
	case "media-title":
		if s, ok := data.(string); ok {
			p.state.Title = cleanTitle(s)
		}
	}

	st := p.state
	now := p.now

	// Throttle history writes to once a second.
	save := now != nil && st.Duration > 0 && st.Pos > 0 && time.Since(p.lastSave) >= time.Second
	if save {
		p.lastSave = time.Now()
	}

	// Look ahead once we're far enough in.
	prefetch := false
	if now != nil && !p.prefetched && p.cfg.AutoNext &&
		now.Queue.HasNext() && st.Duration > 0 &&
		st.Duration-st.Pos <= prefetchLead(st.Duration) {
		p.prefetched = true
		prefetch = true
	}

	// Only repaint when the displayed second actually changes, otherwise mpv's
	// property firehose turns into a Bubble Tea message firehose.
	sec := int(st.Pos)
	repaint := sec != p.lastEmit || name != "time-pos"
	if repaint {
		p.lastEmit = sec
	}
	videoID := ""
	if now != nil {
		videoID = now.VideoID
	}
	p.mu.Unlock()

	if save {
		go UpdatePosition(videoID, st.Pos, st.Duration)
	}
	if prefetch && now != nil {
		p.emit(PrefetchNextMsg{Prev: *now})
	}
	if repaint {
		p.emit(PlayerStateMsg{State: st})
	}
}

func (p *Player) onFileLoaded() {
	p.mu.Lock()
	p.replacing = false
	p.state.Loading = false
	seek := p.seekTo
	p.seekTo = 0
	st := p.state
	p.mu.Unlock()

	// Off the read loop — see sendAsync. Calling command() here blocked the
	// reader for the full timeout, twice on a resume, and no property updates
	// were processed in the meantime.
	p.sendAsync(func() {
		if seek > 5 {
			p.command("seek", seek, "absolute")
		}
		// Loading a new file while paused leaves mpv paused, so you'd have to
		// go and hit play yourself. Picking a stream means you want it to play.
		p.command("set_property", "pause", false)
	})

	p.maybeAutoSubtitle()
	p.maybeAutoAudio()

	p.emit(PlayerStateMsg{State: st})
}

// maybeAutoSubtitle loads a preferred-language subtitle for the file that just
// loaded, once per playback.
//
// It runs off the read loop because GetSubtitles is a network round trip to
// every subtitle addon, and the guard is set before that happens so a stray
// second file-loaded can't fire it twice. A track that shipped with the stream
// is already timed to this exact file and plays via mpv's --slang, so it is
// only worth fetching when the top-preference language didn't ship.
func (p *Player) maybeAutoSubtitle() {
	p.mu.Lock()
	now, cfg := p.now, p.cfg
	gen := p.playGen

	// A per-show preference is explicit intent, so it applies even when
	// autoloading is off. It only exists for a series with a queue; movies
	// and "other" always take the global path below.
	var pref ShowSubPref
	hasPref := false
	if now != nil && now.MediaType == "series" && now.Queue != nil {
		pref, hasPref = GetSubPref(now.Entry.ID)
	}

	// "other" is a debrid library / local file: there is no episode id worth
	// asking a subtitle addon about, so skip it entirely.
	skip := now == nil || p.autoSubbed || now.MediaType == "other" ||
		(!hasPref && !cfg.AutoSubtitle)
	if !skip {
		p.autoSubbed = true
	}
	p.mu.Unlock()

	if skip {
		return
	}

	prefs := PreferredLangs(cfg.SubtitleLang)
	if len(prefs) == 0 && !hasPref {
		return
	}

	// Copy the request: a new play() can overwrite p.now while the addons are
	// being asked, and the subtitle has to match the file that asked for it.
	// The subs screen can replace elements of the shipped list in place, so
	// clone that rather than iterating a slice it may still be writing.
	req := *now
	req.Subs = slices.Clone(now.Subs)

	if !hasPref {
		// A shipped track in the top-preference language is already timed to
		// this exact file and will play via mpv's --slang, so there is nothing
		// worth fetching. A shipped track in a lower-preference language is not
		// enough: the user still wants a plugin track for the top one.
		if PickPreferred(req.Subs, prefs[:1]) != nil {
			return
		}
	}

	// Snapshot the addon list: LoadAddons replaces it wholesale, and this
	// goroutine has no lock under which to read the header.
	addons := slices.Clone(ctx.addons)
	p.sendAsync(func() {
		subs := MergeSubtitles(req.Subs, GetSubtitles(addons, SubsQueryFrom(&req)), cfg.SubtitleLang)

		// A new play() may have started while the network was slow. Its result
		// belongs to a file that is no longer loaded, so drop it.
		p.mu.Lock()
		stale := p.playGen != gen
		p.mu.Unlock()
		if stale {
			return
		}

		shipped := make(map[string]bool, len(req.Subs))
		for _, s := range req.Subs {
			shipped[s.URL] = true
		}

		if hasPref {
			// With autoload off, the show's own language is all we honour:
			// falling back to the global list would be autoloading by another
			// name.
			fallback := prefs
			if !cfg.AutoSubtitle {
				fallback = nil
			}
			p.autoSubtitleForShow(&req, subs, shipped, pref, fallback)
			return
		}

		// Try each language in preference order. A shipped track in one of
		// them wins that tier outright — mpv's --slang plays it, so add
		// nothing. Otherwise the plugin tracks for that language are tried in
		// merged order (a dead link fails inside mpv, so move on to the next).
		// A lower tier is only reached when neither source has the language.
		for _, prefLang := range prefs {
			if PickPreferred(req.Subs, []string{prefLang}) != nil {
				return
			}
			for i := range subs {
				s := subs[i]
				if shipped[s.URL] || langName(s.Lang) != prefLang {
					continue
				}
				if p.addSubtitle(s) {
					return
				}
			}
		}
	})
}

// autoSubtitleForShow applies a per-show preference: the language the user last
// chose for this show, preferring the source they chose it from.
//
// Tiers are the show language first, then the global preference list. The show
// tier tries its recorded source first and the other second; a language absent
// from both sources falls through to the normal per-tier handling for the
// remaining languages.
func (p *Player) autoSubtitleForShow(req *PlayRequest, subs []Subtitle, shipped map[string]bool, pref ShowSubPref, fallback []string) {
	lang := pref.Lang

	tiers := []string{lang}
	for _, l := range fallback {
		if l != lang {
			tiers = append(tiers, l)
		}
	}

	for i, tier := range tiers {
		if i == 0 {
			if p.loadShowTier(req, subs, shipped, tier, pref.Src) {
				return
			}
			continue
		}
		if PickPreferred(req.Subs, []string{tier}) != nil {
			return
		}
		for j := range subs {
			s := subs[j]
			if shipped[s.URL] || langName(s.Lang) != tier {
				continue
			}
			if p.addSubtitle(s) {
				return
			}
		}
	}
}

// loadShowTier loads the show's own language, trying the source the user picked
// first and the other one after. A dead link fails inside mpv, so each
// candidate is tried in turn.
func (p *Player) loadShowTier(req *PlayRequest, subs []Subtitle, shipped map[string]bool, lang, src string) bool {
	embedded := func() bool {
		// The track the user means by "embedded" is usually already in the
		// file, and --slang is global, so a show language ranked low there
		// wouldn't be selected on its own. Set sid explicitly.
		if p.selectEmbeddedSub(lang) {
			return true
		}
		// Otherwise a subtitle shipped with the stream is the next best
		// embedded candidate; it's an external URL, loaded like a plugin one.
		for i := range req.Subs {
			s := req.Subs[i]
			if langName(s.Lang) != lang {
				continue
			}
			if p.addSubtitle(s) {
				return true
			}
		}
		return false
	}
	plugin := func() bool {
		for i := range subs {
			s := subs[i]
			if shipped[s.URL] || langName(s.Lang) != lang {
				continue
			}
			if p.addSubtitle(s) {
				return true
			}
		}
		return false
	}

	if src == "embedded" {
		return embedded() || plugin()
	}
	return plugin() || embedded()
}

// selectEmbeddedSub picks the subtitle track already inside the file whose
// language matches, by setting mpv's sid directly. Mirrors maybeAutoAudio's
// track-list walk; any failure is silent so playback is never broken.
func (p *Player) selectEmbeddedSub(lang string) bool {
	resp, err := p.command("get_property", "track-list")
	if err != nil {
		return false
	}
	raw, _ := resp["data"].([]any)
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != "sub" {
			continue
		}
		l, _ := m["lang"].(string)
		if langName(l) != lang {
			continue
		}
		id, ok := m["id"].(float64)
		if !ok {
			continue
		}
		if _, err := p.command("set_property", "sid", int(id)); err != nil {
			return false
		}
		p.emit(PlayerNoticeMsg{Text: "subtitles: " + lang + " · embedded"})
		return true
	}
	return false
}

// addSubtitle loads a track and, on success, announces it. Shared by the
// global and per-show autoloaders.
func (p *Player) addSubtitle(s Subtitle) bool {
	msg, _ := p.AddSubtitle(s.URL, langName(s.Lang), s.Lang)().(SubtitleAddedMsg)
	if msg.Err != nil {
		return false
	}
	notice := "subtitles: " + langName(s.Lang) + " · " + s.Label()
	if s.Addon != "" {
		notice += " via " + s.Addon
	}
	p.emit(PlayerNoticeMsg{Text: notice})
	p.emit(msg)
	return true
}

// maybeAutoAudio picks the audio track for the file that just loaded.
//
// mpv's --alang is only a preference list; it cannot say "whatever track the
// release tagged as the original", and many rips leave the original dub with
// no language at all. So the order the user set is applied per file here, by
// reading mpv's own track-list. Every failure returns silently — a stream
// without a readable track-list must not break playback.
//
// Ranking: purely the user's preference order; languages not listed come
// last. Ties break on default, then lowest id.
func (p *Player) maybeAutoAudio() {
	p.mu.Lock()
	order := p.cfg.AudioLang
	p.mu.Unlock()

	if order == "" {
		return
	}
	prefs := PreferredLangs(order)
	if len(prefs) == 0 {
		return
	}

	// Off the read loop: command() waits for a reply only readLoop can
	// deliver, so calling it from here in-line would stall every update.
	p.sendAsync(func() {
		resp, err := p.command("get_property", "track-list")
		if err != nil {
			return
		}
		raw, _ := resp["data"].([]any)

		var tracks []audioTrack
		for _, it := range raw {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t != "audio" {
				continue
			}
			id, ok := m["id"].(float64)
			if !ok {
				continue
			}
			lang, _ := m["lang"].(string)
			title, _ := m["title"].(string)
			def, _ := m["default"].(bool)
			if !def {
				// mpv versions disagree on the key: "default" vs "is-default".
				def, _ = m["is-default"].(bool)
			}
			tracks = append(tracks, audioTrack{id: int(id), lang: lang, title: title, def: def})
		}

		// Nothing to choose between.
		if len(tracks) < 2 {
			return
		}

		best := tracks[0]
		for _, t := range tracks[1:] {
			if audioBetter(t, best, prefs) {
				best = t
			}
		}

		cur, err := p.command("get_property", "aid")
		if err != nil {
			return
		}
		if id, _ := cur["data"].(float64); int(id) == best.id {
			return
		}
		p.command("set_property", "aid", best.id)
	})
}

type audioTrack struct {
	id    int
	lang  string
	title string
	def   bool
}

func audioTier(t audioTrack, prefs []string) int {
	name := langName(strings.ToLower(t.lang))
	// Rank purely by the user's preference order; unlisted languages come last.
	if i := slices.Index(prefs, name); i >= 0 {
		return 2 + i
	}
	return 2 + len(prefs)
}

// audioBetter reports whether a should be picked over b.
func audioBetter(a, b audioTrack, prefs []string) bool {
	if ta, tb := audioTier(a, prefs), audioTier(b, prefs); ta != tb {
		return ta < tb
	}
	if a.def != b.def {
		return a.def
	}
	return a.id < b.id
}

func (p *Player) onEndFile(reason string) {
	p.mu.Lock()
	// A `loadfile ... replace` makes mpv emit end-file/stop for the outgoing
	// file. That is bookkeeping, not the end of playback — ignore it.
	if p.replacing {
		p.mu.Unlock()
		return
	}
	now := p.now
	st := p.state
	autoNext := p.cfg.AutoNext
	autoSync := p.cfg.AutoSync
	prefetched := p.prefetched
	queued := p.queued
	p.mu.Unlock()

	if now == nil {
		return
	}

	// One push per file end, whichever branch below returns first. Off the
	// read loop (sendAsync) and silent; an error end never finished anything
	// to push.
	if autoSync && reason != "error" {
		defer p.sendAsync(autoSyncPush)
	}

	// Stamp the final position as the last real change so the push above sees
	// it as newer than the server's copy. Registered after the push defer so
	// it runs first (defers are LIFO), and after the switch's UpdatePosition
	// because defers run on return.
	defer MarkPositionFinal(now.VideoID)

	switch reason {
	case "eof":
		// Watched in full — pin it at 100% so history is unambiguous.
		UpdatePosition(now.VideoID, st.Duration, st.Duration)

		// A queued stream beats everything: you've already chosen, so don't
		// open a picker or announce anything, just play it. Off the read loop,
		// since play() sends a command and this handler runs inside it.
		if queued != nil {
			// Cleared before the play, not by it. play() nils the field, but
			// it runs on another goroutine — until it does, the slot still
			// holds this request, and a second end-file would hand the same
			// one over again. Narrow window, silent double-play.
			p.mu.Lock()
			p.queued = nil
			p.state.NextLabel = ""
			p.mu.Unlock()

			p.sendAsync(func() {
				if msg := p.play(*queued); msg != nil {
					p.emit(msg)
				}
			})
			return
		}

		if autoNext && now.Queue.HasNext() && !prefetched {
			p.emit(EpisodeEndedMsg{Prev: *now})
			return
		}
		if prefetched {
			return // the next episode's stream list is already open
		}
		p.emit(PlayerNoticeMsg{Text: "finished — " + now.Label})
	case "error":
		// Deliberately doesn't skip ahead: a failure here means this stream is
		// bad, so what you want is a different stream for the same episode,
		// which is where you already are.
		p.emit(PlayerErrMsg{Err: errors.New("mpv couldn't play that stream — R to refetch, or pick another")})
	default:
		if st.Duration > 0 && st.Pos > 0 {
			UpdatePosition(now.VideoID, st.Pos, st.Duration)
		}
	}

	p.mu.Lock()
	p.state.Loading = false
	p.mu.Unlock()
}

// ── Public controls ───────────────────────────────────────────────────────────

// Play returns a tea.Cmd so screens can fire it without blocking the UI.
func (p *Player) Play(req PlayRequest) tea.Cmd {
	return func() tea.Msg { return p.play(req) }
}

func (p *Player) play(req PlayRequest) tea.Msg {
	if err := p.connect(); err != nil {
		return PlayerErrMsg{Err: err}
	}

	// Flush the outgoing item's position before we lose track of it.
	p.mu.Lock()
	prev, prevSt := p.now, p.state
	p.replacing = true
	p.prefetched = false
	p.autoSubbed = false
	p.playGen++    // invalidates any subtitle fetch still in flight
	p.queued = nil // choosing something now supersedes whatever was lined up
	p.now = &req
	p.seekTo = req.Resume
	p.state = PlayerState{
		Alive:   true,
		Loading: true,
		Label:   req.Label,
		VideoID: req.VideoID,
	}
	if req.Queue != nil {
		p.state.QueuePos = req.Queue.Index + 1
		p.state.QueueLen = len(req.Queue.Episodes)
	}
	st := p.state
	histMax := p.cfg.HistoryMax
	p.mu.Unlock()

	if prev != nil && prevSt.Duration > 0 && prevSt.Pos > 0 {
		UpdatePosition(prev.VideoID, prevSt.Pos, prevSt.Duration)
	}
	if req.Entry.ID != "" {
		AddHistory(req.Entry, histMax)
	}

	if _, err := p.command("loadfile", req.URL, "replace"); err != nil {
		p.mu.Lock()
		p.replacing = false
		p.state.Loading = false
		p.mu.Unlock()
		return PlayerErrMsg{Err: err}
	}
	return PlayerStateMsg{State: st}
}

// Stop quits mpv outright. `stop` alone leaves the window sitting there when
// the user's mpv.conf sets idle=yes, which looks like the key did nothing.
func (p *Player) Stop() tea.Cmd {
	return func() tea.Msg {
		p.mu.Lock()
		now, st := p.now, p.state
		autoSync := p.cfg.AutoSync
		p.now = nil
		p.state = PlayerState{Alive: st.Alive}
		p.mu.Unlock()

		if now != nil && st.Duration > 0 && st.Pos > 0 {
			UpdatePosition(now.VideoID, st.Pos, st.Duration)
			MarkPositionFinal(now.VideoID)
		}
		if autoSync && now != nil {
			p.sendAsync(autoSyncPush)
		}
		p.command("quit")
		return PlayerStateMsg{}
	}
}

// Quit flushes position and tells mpv to exit.
func (p *Player) Quit() {
	p.Shutdown()
	p.command("quit")
}

// Shutdown flushes the current position without touching mpv.
func (p *Player) Shutdown() {
	p.mu.Lock()
	now, st := p.now, p.state
	p.mu.Unlock()
	if now != nil && st.Duration > 0 && st.Pos > 0 {
		UpdatePosition(now.VideoID, st.Pos, st.Duration)
	}
}
