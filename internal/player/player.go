package player

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"stremio-cliuwu/internal/format"
	"stremio-cliuwu/internal/langs"
	"stremio-cliuwu/internal/queue"
	"stremio-cliuwu/internal/store"
	"stremio-cliuwu/internal/stremio"
	"stremio-cliuwu/internal/subsync"
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
	// Not a fixed name in shared /tmp: another local user could pre-create
	// it, and a second account would attach to this one's mpv.
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "stremio-cliuwu.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("stremio-cliuwu-%d.sock", os.Getuid()))
}

var errNoMpv = errors.New("mpv is not running")

// ── Messages into the Bubble Tea program ──────────────────────────────────────

type StateMsg struct{ State State }
type NoticeMsg struct{ Text string }
type ErrMsg struct{ Err error }

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

type State struct {
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

func (s State) Frac() float64 {
	if s.Duration <= 0 {
		return 0
	}
	return s.Pos / s.Duration
}

func (s State) Percent() float64 { return s.Frac() * 100 }

// ── Play request ──────────────────────────────────────────────────────────────

type PlayRequest struct {
	VideoID   string
	MediaType string
	Label     string
	URL       string
	Resume    float64
	Entry     store.HistoryEntry // written to history when playback starts
	Queue     *queue.EpQueue     // non-nil for series, drives autoplay
	Addons    []stremio.Addon    // needed to resolve the next episode

	// Carried from the chosen stream so the subtitle picker can ask for
	// this exact file rather than the title in general.
	VideoHash string
	VideoSize int64
	Filename  string
	Subs      []stremio.Subtitle // shipped with the stream itself
}

// ── Player ────────────────────────────────────────────────────────────────────

type Player struct {
	cfg  store.AppConfig
	prog *tea.Program

	mu      sync.Mutex
	conn    net.Conn
	pending map[uint32]chan map[string]any
	now     *PlayRequest
	state   State
	seq     uint32

	wmu sync.Mutex // serialises writes to conn

	queued     *PlayRequest           // plays automatically when the current file ends
	replacing  bool                   // suppress the end-file/stop that a replace generates
	prefetched bool                   // next episode already surfaced for this request
	autoSubbed bool                   // subtitle autoload already attempted for this request
	subSeen    bool                   // a subtitle track was active in the current file
	loadedAt   time.Time              // when the current file finished loading
	ownSid     int                    // track id we just selected ourselves; 0 for none
	ownSidTill time.Time              // after this, ownSid no longer excuses a change
	pickSeq    uint64                 // bumped by each subtitle-track pick or off; newest wins
	addonsFn   func() []stremio.Addon // where the subtitle addons come from; fixed at New
	playGen    uint64                 // bumped on every play(); stale subtitle fetches drop out
	seekTo     float64                // pending resume seek, applied on file-loaded
	lastSave   time.Time              // throttles history writes
	lastEmit   int                    // last whole second pushed to the UI
}

// New builds a player. addons says where to find the addons it may ask for
// subtitles: a function rather than a slice because the app replaces its addon
// list wholesale on reload and the player should always see the current one.
// It is a required argument on purpose — without it subtitle autoload would
// quietly find nothing.
func New(cfg store.AppConfig, addons func() []stremio.Addon) *Player {
	return &Player{cfg: cfg, addonsFn: addons, pending: map[uint32]chan map[string]any{}}
}

func (p *Player) Attach(prog *tea.Program) { p.prog = prog }

func (p *Player) currentAddons() []stremio.Addon {
	if p.addonsFn == nil {
		return nil
	}
	return p.addonsFn()
}

func (p *Player) SetConfig(cfg store.AppConfig) { p.mu.Lock(); p.cfg = cfg; p.mu.Unlock() }

func (p *Player) emit(msg tea.Msg) {
	if p.prog != nil {
		p.prog.Send(msg)
	}
}

func (p *Player) State() State {
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

// AddLocalSubtitle loads a subtitle file the app generated into the running
// file and selects it. Unlike AddSubtitle it takes a local path, so it only
// accepts files inside the subsync temp directory: mpv opens whatever it is
// handed, and nothing outside what the app wrote itself should be.
//
// The message's URL is the path, which is how the picker finds it again.
func (p *Player) AddLocalSubtitle(path, title, lang string) tea.Cmd {
	return func() tea.Msg {
		if !subsync.Owns(path) {
			return SubtitleAddedMsg{URL: path, Title: title, Err: errors.New("subtitle file is not one the app generated")}
		}
		if title == "" {
			title = "subtitle"
		}
		_, err := p.commandWait(30*time.Second, "sub-add", path, "select", title, lang)
		return SubtitleAddedMsg{URL: path, Title: title, Err: err}
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
		args = append(args, "--slang="+mpvLangList(cfg.SubtitleLang))
	}
	if cfg.AudioLang != "" {
		args = append(args, "--alang="+mpvLangList(cfg.AudioLang))
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start mpv (%s): %w", bin, err)
	}
	go cmd.Wait() // don't leave a zombie
	return nil
}

// mpvLangList turns the settings text ("eng, en, English") into what mpv wants:
// comma-separated items, which it does not trim itself.
func mpvLangList(s string) string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return strings.Join(out, ",")
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
	p.state = State{}

	// Nothing left to follow. Left set, it would suppress the next-episode
	// picker for the rest of the session — Queued() being non-nil is what
	// tells the app a choice has already been made.
	p.queued = nil
	p.mu.Unlock()

	if now != nil && st.Duration > 0 && st.Pos > 0 {
		store.UpdatePosition(now.VideoID, st.Pos, st.Duration)
	}
	p.emit(StateMsg{})
}

// sendAsync runs IPC work off the read loop.
//
// dispatch is called synchronously from readLoop, and command() waits for a
// reply that only readLoop can deliver — so calling it from an event handler
// deadlocks until the timeout fires. The command still reaches mpv, which is
// why this looked like it worked; what it actually did was stall every
// property update for four seconds after each file load.
func (p *Player) sendAsync(fn func()) { go fn() }

// mpvTitle is what mpv should show as the media title.
//
// Empty leaves mpv to its own behaviour: the filename from the url path, or
// whatever the server says in Content-Disposition, falling back to the url
// itself. For a debrid link that is usually the release name.
func mpvTitle(req PlayRequest) string {
	e := req.Entry

	mode := store.CfgNow().MpvTitle
	if mode == "" || mode == "default" {
		return ""
	}

	// A library file has no metadata behind it. Its episode number is the
	// file's position in the pack, recorded so history can tell the files
	// apart, and its title is the filename — so there is nothing to format
	// or to pattern against, in either mode.
	if e.Type == "other" {
		if t := strings.TrimSpace(e.EpTitle); t != "" {
			return t
		}
		return e.Name
	}

	switch mode {
	case "formatted":
		if e.Episode > 0 {
			out := fmt.Sprintf("%s - %s", e.Name, format.Ep(e.Season, e.Episode, e.VideoID))
			// Only when the addon has a real one. Some fill the field with
			// "Episode #29.1", which says nothing the number has not.
			if t := strings.TrimSpace(e.EpTitle); t != "" && !strings.HasPrefix(t, "Episode #") {
				out += " - " + t
			}
			return out
		}
		if e.Year != "" {
			return fmt.Sprintf("%s (%s)", e.Name, e.Year)
		}
		return e.Name
	}

	// A pattern, using the same placeholders as the download filenames. The
	// segments are joined back with a space: a title is one line, and the
	// separators only mean folders when a path is being built.
	vals := map[string]string{
		"title": e.Name,
		"year":  e.Year,
		"show":  e.Name,
	}
	if e.Episode > 0 {
		vals["season"] = fmt.Sprintf("%02d", e.Season)
		vals["episode"] = fmt.Sprintf("%02d", e.Episode)
		if e.EpTitle != "" {
			vals["title"] = e.EpTitle
		}
	}
	return strings.Join(format.ExpandPattern(mode, vals), " ")
}

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
	props := []string{"time-pos", "duration", "pause", "media-title", "paused-for-cache", "sid"}
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
	showID := ""
	pickShow, pickSid, pickGen, pickSeq := "", 0, uint64(0), uint64(0)
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
			p.state.Title = format.CleanTitle(s)
		}
	case "sid":
		// A track number means subtitles are on (autoloaded or hand-picked).
		// mpv reports "no" as false: turning subtitles off after they had
		// been on is the user saying this show wants none. Null (no file) and
		// anything during a load are not that.
		if v, on := data.(float64); on {
			p.subSeen = true
			switch {
			case p.ownSid == int(v) && time.Now().Before(p.ownSidTill):
				// Our own autoloader selected this one.
				p.ownSid = 0
			case !p.state.Loading && !p.replacing && !p.loadedAt.IsZero() &&
				time.Since(p.loadedAt) > handPickAfter &&
				p.now != nil && p.now.MediaType == "series" && p.now.Queue != nil:
				// A track switch well after load is the user using mpv's own
				// menu: mpv's --slang pick and our autoloader both land
				// within the first moments.
				p.pickSeq++
				pickShow, pickSid, pickGen, pickSeq = p.now.Entry.ID, int(v), p.playGen, p.pickSeq
			}
		} else if off, ok := data.(bool); ok && !off && p.subSeen &&
			!p.state.Loading && !p.replacing &&
			p.now != nil && p.now.MediaType == "series" && p.now.Queue != nil {
			p.subSeen = false
			p.pickSeq++ // a pick still being resolved is older than this
			showID = p.now.Entry.ID
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
		st.Duration-st.Pos <= format.PrefetchLead(st.Duration) {
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

	if showID != "" {
		go store.SetSubPref(showID, store.SubOff, "")
	}
	if pickShow != "" {
		p.sendAsync(func() { p.rememberEmbeddedPick(pickGen, pickSeq, pickShow, pickSid) })
	}
	if save {
		go store.UpdatePosition(videoID, st.Pos, st.Duration)
	}
	if prefetch && now != nil {
		p.emit(PrefetchNextMsg{Prev: *now})
	}
	if repaint {
		p.emit(StateMsg{State: st})
	}
}

func (p *Player) onFileLoaded() {
	p.mu.Lock()
	p.replacing = false
	p.state.Loading = false
	seek := p.seekTo
	p.seekTo = 0
	p.loadedAt = time.Now()
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

	p.emit(StateMsg{State: st})
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
	var pref store.ShowSubPref
	hasPref := false
	if now != nil && now.MediaType == "series" && now.Queue != nil {
		pref, hasPref = store.GetSubPref(now.Entry.ID)
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

	// This show was last left with subtitles off. mpv's --slang may still have
	// picked an embedded track on its own, so switch it off explicitly.
	if hasPref && pref.Lang == store.SubOff {
		p.sendAsync(func() {
			p.mu.Lock()
			stale := p.playGen != gen
			p.mu.Unlock()
			if !stale {
				p.command("set_property", "sid", "no")
			}
		})
		return
	}

	prefs := langs.Preferred(cfg.SubtitleLang)
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
		if stremio.PickPreferred(req.Subs, prefs[:1]) != nil {
			return
		}
	}

	// Snapshot the addon list: LoadAddons replaces it wholesale, and this
	// goroutine has no lock under which to read the header.
	addons := slices.Clone(p.currentAddons())
	p.sendAsync(func() {
		// A subtitle the user already synced for this video is retimed to this
		// file and costs no network, so it beats everything else. It has to be
		// in a language the user wants: the show's own, else the global list.
		if p.loadSynced(gen, req.VideoID, func(lang string) bool {
			if hasPref {
				return langs.Name(lang) == pref.Lang
			}
			return slices.Contains(prefs, langs.Name(lang))
		}) {
			return
		}

		// What is already in the file needs no network, so it goes first: the
		// show's language shouldn't wait on the slowest subtitle addon while
		// --slang's own pick is on screen.
		if hasPref && p.tryEmbedded(gen, &req, pref.Lang, pref.Track) {
			return
		}

		subs := stremio.MergeSubtitles(req.Subs, stremio.GetSubtitles(addons, SubsQueryFrom(&req)), cfg.SubtitleLang)

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
			p.autoSubtitleForShow(gen, &req, subs, shipped, pref, fallback)
			return
		}

		// Try each language in preference order. A shipped track in one of
		// them wins that tier outright — mpv's --slang plays it, so add
		// nothing. Otherwise the plugin tracks for that language are tried in
		// merged order (a dead link fails inside mpv, so move on to the next).
		// A lower tier is only reached when neither source has the language.
		for _, prefLang := range prefs {
			if stremio.PickPreferred(req.Subs, []string{prefLang}) != nil {
				return
			}
			for i := range subs {
				s := subs[i]
				if shipped[s.URL] || langs.Name(s.Lang) != prefLang {
					continue
				}
				if p.addSubtitle(gen, s) {
					return
				}
			}
		}
	})
}

// autoSubtitleForShow applies a per-show preference: the language the user last
// chose for this show.
//
// Tiers are the show language first, then the global preference list. The
// caller has already tried the show language inside the file (tryEmbedded), so
// its tier here is the plugins; a language absent from both falls through to
// the normal per-tier handling for the remaining languages.
func (p *Player) autoSubtitleForShow(gen uint64, req *PlayRequest, subs []stremio.Subtitle, shipped map[string]bool, pref store.ShowSubPref, fallback []string) {
	lang := pref.Lang

	tiers := []string{lang}
	for _, l := range fallback {
		if l != lang {
			tiers = append(tiers, l)
		}
	}

	for i, tier := range tiers {
		if i == 0 {
			if p.loadShowPlugin(gen, subs, shipped, tier) {
				return
			}
			continue
		}
		if stremio.PickPreferred(req.Subs, []string{tier}) != nil {
			return
		}
		for j := range subs {
			s := subs[j]
			if shipped[s.URL] || langs.Name(s.Lang) != tier {
				continue
			}
			if p.addSubtitle(gen, s) {
				return
			}
		}
	}
}

// tryEmbedded loads the show's language from what needs no fetch: a track
// inside the file, else one shipped with the stream. A track that is already
// timed to this exact file beats a fetched one whatever the show was last
// picked from.
func (p *Player) tryEmbedded(gen uint64, req *PlayRequest, lang, track string) bool {
	// The track the user means by "embedded" is usually already in the file,
	// and --slang is global, so a show language ranked low there wouldn't be
	// selected on its own. Set sid explicitly.
	if p.selectEmbeddedSub(gen, lang, track) {
		return true
	}
	// A subtitle shipped with the stream is the next best embedded candidate;
	// it's an external URL, loaded like a plugin one.
	for i := range req.Subs {
		s := req.Subs[i]
		if langs.Name(s.Lang) != lang {
			continue
		}
		if p.addSubtitle(gen, s) {
			return true
		}
	}
	return false
}

// loadShowPlugin loads the show's language from the fetched plugin tracks. A
// dead link fails inside mpv, so each candidate is tried in turn.
func (p *Player) loadShowPlugin(gen uint64, subs []stremio.Subtitle, shipped map[string]bool, lang string) bool {
	for i := range subs {
		s := subs[i]
		if shipped[s.URL] || langs.Name(s.Lang) != lang {
			continue
		}
		if p.addSubtitle(gen, s) {
			return true
		}
	}
	return false
}

// selectEmbeddedSub picks the subtitle track already inside the file for this
// language (and title, when several share it), by setting mpv's sid directly.
// Any failure is silent so playback is never broken.
func (p *Player) selectEmbeddedSub(gen uint64, lang, title string) bool {
	if !p.isCurrent(gen) {
		return false
	}
	resp, err := p.command("get_property", "track-list")
	if err != nil {
		return false
	}
	raw, _ := resp["data"].([]any)
	id, selected, ok := pickEmbeddedSub(raw, lang, title)
	if !ok {
		return false
	}
	if !selected {
		// Armed only when a change is coming — setting the track that is
		// already on makes no event to consume it — and disarmed if the
		// command fails, so it can't excuse a later hand pick.
		p.mu.Lock()
		p.ownSid, p.ownSidTill = id, time.Now().Add(ownSidWindow)
		p.mu.Unlock()
		if _, err := p.command("set_property", "sid", id); err != nil {
			p.mu.Lock()
			p.ownSid = 0
			p.mu.Unlock()
			return false
		}
	}
	p.emit(NoticeMsg{Text: "subtitles: " + lang + " · embedded"})
	return true
}

const (
	// handPickAfter is how long after a file loads a subtitle-track change
	// still counts as mpv's --slang pick or our own autoload rather than the
	// user's. Generous, because a remote stream can settle its tracks late;
	// a real pick inside it is simply not remembered.
	handPickAfter = 3 * time.Second
	// ownSidWindow is how long our own track selection waits for its event
	// to come back through a busy read loop.
	ownSidWindow = 5 * time.Second
)

// rememberEmbeddedPick records a track the user switched to in mpv's own
// menu, so the next episode of the show starts on it. Only a track inside the
// file qualifies; see embeddedSubAt. Runs off the read loop (it asks mpv).
//
// seq is the pick's place in line: while this waits on mpv the user may have
// picked again or switched subtitles off, and the newer one has to win.
func (p *Player) rememberEmbeddedPick(gen, seq uint64, showID string, sid int) {
	resp, err := p.command("get_property", "track-list")
	if err != nil {
		return
	}
	p.mu.Lock()
	stale := p.playGen != gen || p.pickSeq != seq
	p.mu.Unlock()
	if stale {
		return
	}
	raw, _ := resp["data"].([]any)
	if lang, title, ok := embeddedSubAt(raw, sid); ok {
		store.SetEmbeddedPref(showID, lang, title)
	}
}

// subTrack is one subtitle entry from mpv's track-list.
type subTrack struct {
	id       int
	lang     string // canonical name, "" when the file does not tag one
	title    string
	external bool
	selected bool
}

// subTracks reads the subtitle entries out of mpv's track-list.
func subTracks(tracks []any) []subTrack {
	var out []subTrack
	for _, it := range tracks {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != "sub" {
			continue
		}
		id, ok := m["id"].(float64)
		if !ok {
			continue
		}
		st := subTrack{id: int(id)}
		if l, _ := m["lang"].(string); strings.TrimSpace(l) != "" {
			st.lang = langs.Name(l)
		}
		st.title, _ = m["title"].(string)
		st.external, _ = m["external"].(bool)
		st.selected, _ = m["selected"].(bool)
		out = append(out, st)
	}
	return out
}

// embeddedSubAt reports the language and title of the subtitle track with this
// id when it is a track inside the file. External tracks are what our own
// sub-add made, and a track with no language tag could never be matched on
// another episode, so neither is worth remembering.
func embeddedSubAt(tracks []any, sid int) (lang, title string, ok bool) {
	for _, t := range subTracks(tracks) {
		if t.id != sid {
			continue
		}
		if t.external || t.lang == "" {
			return "", "", false
		}
		return t.lang, t.title, true
	}
	return "", "", false
}

// pickEmbeddedSub finds the in-file subtitle track for a language. When
// several share it, the one with the remembered title wins and the first is the
// fallback. selected reports that mpv already has it on.
func pickEmbeddedSub(tracks []any, lang, title string) (id int, selected, ok bool) {
	for _, t := range subTracks(tracks) {
		if t.external || t.lang != lang {
			continue
		}
		if !ok {
			id, selected, ok = t.id, t.selected, true
		}
		if title != "" && t.title == title {
			return t.id, t.selected, true
		}
	}
	return id, selected, ok
}

// isCurrent reports whether the file that started an autoload is still the one
// playing. A subtitle fetch or sub-add can outlast an episode change, and a
// track added after it would attach to the wrong file.
func (p *Player) isCurrent(gen uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playGen == gen
}

// loadSynced loads the newest subtitle the user synced for this video, if one
// exists in an accepted language. The announced message carries the original
// subtitle's URL, so the picker marks the right row.
func (p *Player) loadSynced(gen uint64, videoID string, accept func(lang string) bool) bool {
	if !p.isCurrent(gen) {
		return false
	}
	syn, ok := subsync.Latest(videoID, accept)
	if !ok {
		return false
	}
	msg, _ := p.AddLocalSubtitle(syn.Path, langs.Name(syn.Lang), syn.Lang)().(SubtitleAddedMsg)
	if msg.Err != nil {
		return false
	}
	p.emit(NoticeMsg{Text: "subtitles: " + langs.Name(syn.Lang) + " · synced"})
	msg.URL = syn.URL
	p.emit(msg)
	return true
}

// addSubtitle loads a track and, on success, announces it. Shared by the
// global and per-show autoloaders.
func (p *Player) addSubtitle(gen uint64, s stremio.Subtitle) bool {
	if !p.isCurrent(gen) {
		return false
	}
	msg, _ := p.AddSubtitle(s.URL, langs.Name(s.Lang), s.Lang)().(SubtitleAddedMsg)
	if msg.Err != nil {
		return false
	}
	notice := "subtitles: " + langs.Name(s.Lang) + " · " + s.Label()
	if s.Addon != "" {
		notice += " via " + s.Addon
	}
	p.emit(NoticeMsg{Text: notice})
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
	prefs := langs.Preferred(order)
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
	name := langs.Name(strings.ToLower(t.lang))
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
		if reason != "error" {
			p.mu.Unlock()
			return
		}
		// The outgoing file ends with "stop"; an error here is the new file
		// failing to open, which never reaches file-loaded to clear this.
		p.replacing = false
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
		defer p.sendAsync(store.AutoSyncPush)
	}

	// Stamp the final position as the last real change so the push above sees
	// it as newer than the server's copy. Registered after the push defer so
	// it runs first (defers are LIFO), and after the switch's UpdatePosition
	// because defers run on return.
	defer store.MarkPositionFinal(now.VideoID)

	switch reason {
	case "eof":
		// Watched in full — pin it at 100% so history is unambiguous.
		store.UpdatePosition(now.VideoID, st.Duration, st.Duration)

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
		p.emit(NoticeMsg{Text: "finished — " + now.Label})
	case "error":
		// Deliberately doesn't skip ahead: a failure here means this stream is
		// bad, so what you want is a different stream for the same episode,
		// which is where you already are.
		p.emit(ErrMsg{Err: errors.New("mpv couldn't play that stream — R to refetch, or pick another")})
	default:
		if st.Duration > 0 && st.Pos > 0 {
			store.UpdatePosition(now.VideoID, st.Pos, st.Duration)
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
		return ErrMsg{Err: err}
	}

	// Flush the outgoing item's position before we lose track of it.
	p.mu.Lock()
	prev, prevSt := p.now, p.state
	p.replacing = true
	p.prefetched = false
	p.autoSubbed = false
	p.subSeen = false
	p.loadedAt = time.Time{}
	p.ownSid = 0
	p.playGen++    // invalidates any subtitle fetch still in flight
	p.queued = nil // choosing something now supersedes whatever was lined up
	p.now = &req
	p.seekTo = req.Resume
	p.state = State{
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
		store.UpdatePosition(prev.VideoID, prevSt.Pos, prevSt.Duration)
	}
	if req.Entry.ID != "" {
		store.AddHistory(req.Entry, histMax)
	}

	// Set before the load so mpv has it for the first frame. It is a global
	// property rather than a per-file one, so it survives the loadfile.
	if title := mpvTitle(req); title != "" {
		p.command("set_property", "force-media-title", title)
	} else {
		p.command("set_property", "force-media-title", "")
	}

	// Track selection is sticky in mpv: switching subtitles off with the sid
	// property changes the option itself, so every later file starts with
	// them off too, whatever show it belongs to. Say what this one should
	// start with. "no" for a show left with subtitles off, so they never
	// flash on before the autoloader runs; "auto" otherwise, which restores
	// --slang selection.
	sidMode := "auto"
	if req.MediaType == "series" && req.Queue != nil {
		if pref, ok := store.GetSubPref(req.Entry.ID); ok && pref.Lang == store.SubOff {
			sidMode = "no"
		}
	}
	p.command("set_property", "sid", sidMode)

	if _, err := p.command("loadfile", req.URL, "replace"); err != nil {
		p.mu.Lock()
		p.replacing = false
		p.state.Loading = false
		p.mu.Unlock()
		return ErrMsg{Err: err}
	}
	return StateMsg{State: st}
}

// Stop quits mpv outright. `stop` alone leaves the window sitting there when
// the user's mpv.conf sets idle=yes, which looks like the key did nothing.
func (p *Player) Stop() tea.Cmd {
	return func() tea.Msg {
		p.mu.Lock()
		now, st := p.now, p.state
		autoSync := p.cfg.AutoSync
		p.now = nil
		p.state = State{Alive: st.Alive}
		p.mu.Unlock()

		if now != nil && st.Duration > 0 && st.Pos > 0 {
			store.UpdatePosition(now.VideoID, st.Pos, st.Duration)
			store.MarkPositionFinal(now.VideoID)
		}
		if autoSync && now != nil {
			p.sendAsync(store.AutoSyncPush)
		}
		p.command("quit")
		return StateMsg{}
	}
}

// Resume un-pauses a paused file, so closing the subtitle picker doesn't
// leave mpv sitting there. Same property the file-loaded handler sets.
func (p *Player) Resume() tea.Cmd {
	return func() tea.Msg {
		if _, err := p.command("set_property", "pause", false); err != nil {
			return ErrMsg{Err: err}
		}
		return nil
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
		store.UpdatePosition(now.VideoID, st.Pos, st.Duration)
	}
}
