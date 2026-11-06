package ui

import (
	"fmt"
	"slices"
	"stremio-cliuwu/internal/download"
	"stremio-cliuwu/internal/format"
	"stremio-cliuwu/internal/player"
	"stremio-cliuwu/internal/store"
	"stremio-cliuwu/internal/streams"
	"stremio-cliuwu/internal/stremio"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Shared context ────────────────────────────────────────────────────────────

// appCtx is the small pile of state every screen needs. A package-level
// pointer beats threading five fields through every constructor.
type appCtx struct {
	cfg        store.AppConfig
	refs       stremio.AddonList
	addons     []stremio.Addon
	player     *player.Player
	downloader *download.Downloader
	prog       *tea.Program

	loading bool // manifests still being fetched

	addonsMu sync.RWMutex // guards addons, which goroutines read while reloads replace it
}

// Addons returns the loaded addons. The slice is replaced wholesale on reload,
// never edited, so holding on to it is safe.
func (c *appCtx) Addons() []stremio.Addon {
	c.addonsMu.RLock()
	defer c.addonsMu.RUnlock()
	return c.addons
}

func (c *appCtx) setAddons(a []stremio.Addon) {
	c.addonsMu.Lock()
	c.addons = a
	c.addonsMu.Unlock()
}

var ctx *appCtx

// StreamAddons returns addons that can actually serve streams.
func (c *appCtx) StreamAddons() []stremio.Addon {
	var out []stremio.Addon
	for _, a := range c.Addons() {
		if a.Err == nil && a.HasStreams() {
			out = append(out, a)
		}
	}
	return out
}

// ── Screen interface ──────────────────────────────────────────────────────────

type screen interface {
	Init() tea.Cmd
	Update(tea.Msg) (screen, tea.Cmd)
	View() string
	Title() string  // breadcrumb
	Footer() string // key hints
	SetSize(w, h int)
	Typing() bool // suppress global single-key bindings
}

// backHandler lets a screen consume the global back key for its own internal
// navigation instead of being popped off the stack.
type backHandler interface {
	HandleBack() bool
}

// baseScreen supplies the boring half of the interface.
type baseScreen struct{ w, h int }

func (b *baseScreen) SetSize(w, h int) { b.w, b.h = w, h }
func (b *baseScreen) Typing() bool     { return false }
func (b *baseScreen) Footer() string   { return "" }

// ── Navigation messages ───────────────────────────────────────────────────────

type pushMsg struct{ s screen }
type popMsg struct{ n int }
type popRootMsg struct{}
type replaceMsg struct{ s screen }
type toastMsg struct {
	text  string
	isErr bool
}
type toastExpireMsg struct{ at time.Time }
type reloadAddonsMsg struct{}

// addonsReloadedMsg carries a finished reload back to the UI goroutine, which
// is the only place ctx.refs and ctx.addons are written.
type addonsReloadedMsg struct {
	refs   stremio.AddonList
	addons []stremio.Addon
}

// themeChangedMsg forces every screen to re-render its rows.
//
// Screens bake styled text into Item.Label when they build their list, so
// swapping the accent styles doesn't touch strings that were rendered
// earlier — the menu would keep its old colour until you navigated away and
// back. Section headers updated immediately because those are rendered live
// in the row painter, which is what made the inconsistency visible.
type themeChangedMsg struct{}

// addonsReadyMsg carries the manifests once the background fetch is done.
type addonsReadyMsg struct{ Addons []stremio.Addon }

func themeChanged() tea.Cmd { return func() tea.Msg { return themeChangedMsg{} } }

// rebuildable is any screen that can regenerate its rows on demand.
type rebuildable interface{ rebuild() }

// resyncable is any screen with an info panel to refresh.
type resyncable interface{ syncInfo() tea.Cmd }

func push(s screen) tea.Cmd       { return func() tea.Msg { return pushMsg{s} } }
func pop() tea.Cmd                { return func() tea.Msg { return popMsg{1} } }
func popN(n int) tea.Cmd          { return func() tea.Msg { return popMsg{n} } }
func popRoot() tea.Cmd            { return func() tea.Msg { return popRootMsg{} } }
func replaceTop(s screen) tea.Cmd { return func() tea.Msg { return replaceMsg{s} } }
func toast(s string) tea.Cmd      { return func() tea.Msg { return toastMsg{text: s} } }

// playerStateCmd refreshes the player bar from a screen.
//
// Player methods called from the UI must not emit directly — Send blocks when
// invoked from inside Update. Returning a command hands the message to the
// runtime, which delivers it once the current update has finished.
func playerStateCmd() tea.Cmd {
	return func() tea.Msg { return player.StateMsg{State: ctx.player.State()} }
}
func toastErr(s string) tea.Cmd { return func() tea.Msg { return toastMsg{text: s, isErr: true} } }

// ── Root model ────────────────────────────────────────────────────────────────

type app struct {
	stack  []screen
	w, h   int
	pstate player.State

	toastText string
	toastErr  bool
	toastAt   time.Time

	startup func() tea.Cmd
}

func newApp(root screen) *app { return &app{stack: []screen{root}} }

// startup runs once the program is up: used to jump straight to a search or a
// catalog from the command line.
//
// It is a function because the screens it opens read the installed addons,
// which are not loaded until addonsReadyMsg: built any earlier, "-m" found no
// catalogs and "-s" searched nothing.
func (a *app) setStartup(f func() tea.Cmd) { a.startup = f }

func (a *app) top() screen { return a.stack[len(a.stack)-1] }

func (a *app) Init() tea.Cmd { return a.top().Init() }

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {

	case tea.WindowSizeMsg:
		a.w, a.h = m.Width, m.Height
		a.resize()
		// Screens may need to react — the info pane opens itself on a wide
		// enough terminal and then wants meta for the highlighted row.
		next, cmd := a.top().Update(msg)
		a.stack[len(a.stack)-1] = next
		return a, cmd

	case player.StateMsg:
		a.pstate = m.State
		a.resize()
		// Fall through to the top screen as well: episode lists and the main
		// menu redraw their progress readouts off this.
		next, cmd := a.top().Update(msg)
		a.stack[len(a.stack)-1] = next
		return a, cmd

	case player.NoticeMsg:
		return a, toast(m.Text)

	case download.TickMsg:
		next, cmd := a.top().Update(msg)
		a.stack[len(a.stack)-1] = next
		a.resize()
		return a, cmd

	case download.DoneMsg:
		if m.Err != nil {
			return a, toastErr("download failed — " + m.Err.Error())
		}
		return a, toast("downloaded " + m.Label)

	case player.PrefetchNextMsg:
		// Something's already lined up — don't pull the rug out from under a
		// decision that's been made.
		if ctx.player.Queued() != nil {
			return a, nil
		}
		// The list still opens, so a pick can be changed; the autopick just
		// gets queued behind the current episode before it ends.
		pick := autoPickCmd(m.Prev, true)
		return a, tea.Batch(a.advanceTo(m.Prev, "next up — ", pick == nil), pick)

	case player.EpisodeEndedMsg:
		if ctx.player.Queued() != nil {
			return a, nil
		}
		// Prefetch never fired (a very short episode), so nothing is lined up
		// and this one has already ended. With autoplay on the pick plays as
		// soon as it is made, and the list opens only if nothing was safe.
		if pick := autoPickCmd(m.Prev, false); pick != nil {
			return a, pick
		}
		return a, a.advanceTo(m.Prev, "next up — ", true)

	case autoPickedMsg:
		if !ctx.cfg.AutoPlayNext {
			return a, nil
		}
		// The fetch can outlast the episode: drop the result if you've since
		// started something else or closed mpv.
		current, ended := playerFollowing(m.prev.VideoID)
		if !current {
			return a, nil
		}
		if !m.ok {
			if m.listShown {
				return a, nil
			}
			return a, a.advanceTo(m.prev, "next up — ", true)
		}
		// Whatever you queued yourself in the meantime wins.
		if ctx.player.Queued() != nil {
			return a, nil
		}
		req := buildRequest(m.target, m.stream, 0)
		if ended {
			// It finished while the streams were loading, so nothing would
			// ever consume a queued item.
			return a, tea.Batch(ctx.player.Play(req), toast("playing next — "+req.Label))
		}
		ctx.player.Queue(req)
		return a, tea.Batch(toast("up next — "+req.Label+" · picked for you"), playerStateCmd())

	case player.ErrMsg:
		if m.Err != nil {
			return a, toastErr(m.Err.Error())
		}
		return a, nil

	case toastMsg:
		a.toastText, a.toastErr, a.toastAt = m.text, m.isErr, time.Now()
		at := a.toastAt
		a.resize()
		return a, tea.Tick(4*time.Second, func(time.Time) tea.Msg {
			return toastExpireMsg{at: at}
		})

	case toastExpireMsg:
		if m.at.Equal(a.toastAt) {
			a.toastText = ""
			a.resize()
		}
		return a, nil

	case pushMsg:
		m.s.SetSize(a.w, a.bodyHeight())
		a.stack = append(a.stack, m.s)
		return a, m.s.Init()

	case popMsg:
		for i := 0; i < m.n && len(a.stack) > 1; i++ {
			// Cleared, not just sliced off. Shrinking a slice leaves the
			// element in the backing array, so a popped catalog screen keeps
			// its metas, its info pane and its poster state reachable until
			// something happens to be pushed into that slot.
			a.stack[len(a.stack)-1] = nil
			a.stack = a.stack[:len(a.stack)-1]
		}
		a.resize()
		return a, a.refreshTop()

	case popRootMsg:
		clear(a.stack[1:])
		a.stack = a.stack[:1]
		a.resize()
		return a, a.refreshTop()

	case replaceMsg:
		m.s.SetSize(a.w, a.bodyHeight())
		a.stack[len(a.stack)-1] = m.s
		return a, m.s.Init()

	case addonsReadyMsg:
		ctx.setAddons(m.Addons)
		ctx.loading = false
		for _, sc := range a.stack {
			if r, ok := sc.(rebuildable); ok {
				r.rebuild()
			}
		}
		// Delegated as well as broadcast: anything that tried to fetch before
		// the addons existed got nothing back, and needs telling to try again.
		next, cmd := a.top().Update(msg)
		a.stack[len(a.stack)-1] = next
		if a.startup != nil {
			start := a.startup
			a.startup = nil
			cmd = tea.Batch(cmd, start())
		}
		return a, cmd

	case themeChangedMsg:
		for _, sc := range a.stack {
			if r, ok := sc.(rebuildable); ok {
				r.rebuild()
			}
		}
		return a, nil

	case store.HistorySyncedMsg:
		// A background pull rewrote history underneath every cached row, so
		// rebuild the stack and refetch the top screen's info panel.
		store.InvalidateInProgress()
		for _, sc := range a.stack {
			if r, ok := sc.(rebuildable); ok {
				r.rebuild()
			}
		}
		return a, a.refreshTop()

	case reloadAddonsMsg:
		// Fetching manifests takes seconds; doing it here froze the whole UI.
		return a, func() tea.Msg {
			refs := stremio.LoadAddonRefs()
			return addonsReloadedMsg{refs: refs, addons: stremio.LoadAddons(refs)}
		}

	case addonsReloadedMsg:
		ctx.refs = m.refs
		ctx.setAddons(m.addons)
		// Cached results carry the ranks they were fetched with, so without
		// this a reorder wouldn't take effect until the five minute TTL ran
		// out — which reads as the reorder having done nothing.
		stremio.InvalidateCaches()
		return a, nil

	case tea.KeyMsg:
		if cmd, handled := a.globalKey(m); handled {
			return a, cmd
		}
	}

	next, cmd := a.top().Update(msg)
	a.stack[len(a.stack)-1] = next
	return a, cmd
}

// globalHints are the keys that work on every screen, listed in the overlay
// because the footer only mentions them when they apply.
func (a *app) globalHints() [][2]string {
	out := [][2]string{{"?", "this list"}, {"ctrl+q", "quit"}}
	if a.pstate.Alive {
		out = append(out,
			[2]string{"S", "subtitles"},
			[2]string{"X", "stop mpv"})
	}
	return out
}

func (a *app) globalKey(k tea.KeyMsg) (tea.Cmd, bool) {
	if k.String() == "ctrl+c" {
		return tea.Quit, true
	}
	if a.top().Typing() {
		return nil, false
	}

	switch k.String() {
	case "b":
		// Back, from anywhere. Typing() above keeps this out of text fields.
		//
		// A screen with its own internal hierarchy — the folder browser — gets
		// first refusal, so `b` walks up a directory before leaving.
		if bh, ok := a.top().(backHandler); ok && bh.HandleBack() {
			return nil, true
		}
		if len(a.stack) > 1 {
			return pop(), true
		}
	case "?":
		// Snapshotted before the push: the overlay's own footer calls
		// keyHint, which would otherwise replace the list it is meant to be
		// showing.
		if _, already := a.top().(*keysScreen); already {
			return nil, false
		}
		// Globals the screen already lists are not repeated: the menu names
		// ctrl+q itself, being the one place quitting is the obvious thing
		// to do.
		keys := append([][2]string{}, footerKeys...)
		seen := map[string]bool{}
		for _, p := range keys {
			seen[p[0]] = true
		}
		for _, p := range a.globalHints() {
			if !seen[p[0]] {
				keys = append(keys, p)
			}
		}
		return push(newKeysScreen(a.top().Title(), keys)), true

	case "ctrl+q":
		return quitCmd(), true
	// Pause and seek are deliberately absent: mpv already owns those, and
	// having two sets of bindings for the same thing just invites drift.
	case "X":
		if a.pstate.Alive {
			return ctx.player.Stop(), true
		}
	case "S":
		// The subtitle list has its own S (sync to the reference). Global keys
		// run first, so without this it would open a second list on top.
		if _, here := a.top().(*subsScreen); here {
			return nil, false
		}
		// Global on purpose: you realise you want subtitles a minute into an
		// episode, by which point the stream list is several screens back.
		if now := ctx.player.Now(); now != nil {
			showID := ""
			if now.MediaType == "series" && now.Queue != nil {
				showID = now.Entry.ID
			}
			return push(newSubsScreen(now.Label, player.SubsQueryFrom(now), now.Subs, showID)), true
		}
	}
	return nil, false
}

// autoPickedMsg carries the stream chosen for the next episode, or ok=false
// when nothing in its list was a safe pick.
type autoPickedMsg struct {
	prev      player.PlayRequest // the episode this was fetched for
	target    streamTarget
	stream    stremio.Stream
	ok        bool
	listShown bool // the next episode's list was already surfaced at prefetch
}

// playerFollowing is the player's view of an episode; a seam for tests.
var playerFollowing = func(videoID string) (current, ended bool) {
	return ctx.player.Following(videoID)
}

// autoPickCmd fetches the next episode's streams and picks one, if autoplay is
// on and the episode exists. What the worker needs from ctx is copied first:
// it runs off the UI goroutine.
func autoPickCmd(prev player.PlayRequest, listShown bool) tea.Cmd {
	if !ctx.cfg.AutoPlayNext || !ctx.cfg.AutoNext || prev.Queue == nil {
		return nil
	}
	t, ok := nextEpisodeTarget(prev)
	if !ok {
		return nil
	}
	addons := ctx.StreamAddons()
	preferred, cachedFirst := ctx.cfg.PreferredQuality, ctx.cfg.CachedFirst
	blocked := slices.Clone(ctx.cfg.Blocked)
	binge := store.GetBinge(t.Meta.ID)
	return func() tea.Msg {
		all := stremio.GetStreams(addons, t.MediaType, t.fetchID())
		st, ok := streams.AutoPick(all, preferred, binge, cachedFirst, blocked)
		return autoPickedMsg{prev: prev, target: t, stream: st, ok: ok, listShown: listShown}
	}
}

// advanceTo moves on to the next episode.
//
// It only takes over the screen if you're already sitting on the stream list
// for the episode that just played — otherwise you'd get yanked out of the
// menu, or out of a completely different show, mid-browse. When you're
// elsewhere the streams are fetched in the background instead, so they're
// warm when you do go looking, and the menu's "next up" entry points at them.
func (a *app) advanceTo(prev player.PlayRequest, prefix string, warm bool) tea.Cmd {
	t, ok := nextEpisodeTarget(prev)
	if !ok {
		return toast("finished — " + prev.Label)
	}

	if cur, isStreams := a.top().(*streamScreen); isStreams && cur.target.VideoID == prev.VideoID {
		return tea.Sequence(
			toast(prefix+t.Label),
			replaceTop(newStreamScreenFiltered(t, cur.list.Query())),
		)
	}

	addons := ctx.StreamAddons()
	if warm { // an autopick fetching the same list would make this a duplicate
		go stremio.GetStreams(addons, "series", t.fetchID()) // warm the cache, don't navigate
	}

	return toast(prefix + t.Label + " · ready when you are")
}

// refreshTop rebuilds whichever screen we've just come back to.
//
// Screens cache their rows, and only the top screen receives messages — so a
// buried screen misses everything that happened while it was buried. The main
// menu is the obvious casualty: it sits at the bottom of the stack from
// startup, so its continue-watching entry stayed frozen at whatever it said
// when the app launched, even though history was being written correctly all
// along.
func (a *app) refreshTop() tea.Cmd {
	if r, ok := a.top().(rebuildable); ok {
		r.rebuild()
	}
	// Panels are refreshed too: a screen that was skipped past on the way in
	// never got to load its own info, and one that sat buried may be showing
	// something stale.
	if r, ok := a.top().(resyncable); ok {
		return r.syncInfo()
	}
	return nil
}

// quitCmd confirms first when something is playing, since quitting takes mpv
// with it under the default settings, and when downloads are still running,
// since the queue lives in this process and goes with it.
func quitCmd() tea.Cmd {
	playing := ctx.player.State().Alive
	pending := 0
	if ctx.downloader != nil {
		pending = ctx.downloader.Pending()
	}

	if !playing && pending == 0 {
		return tea.Quit
	}

	var msg, noLabel string
	switch {
	case playing && pending > 0:
		// The part files survive, so this is interrupted rather than lost.
		msg = fmt.Sprintf("stop watching and quit? %s still downloading.", format.Plural(pending, "file"))
		noLabel = "keep watching"
		if !ctx.cfg.CloseMpvOnExit {
			msg = fmt.Sprintf("quit? mpv keeps playing, but %s still downloading.", format.Plural(pending, "file"))
			noLabel = "stay"
		}
	case pending > 0:
		msg = fmt.Sprintf("quit? %s still downloading — they'll resume next time.", format.Plural(pending, "file"))
		noLabel = "stay"
	default:
		msg = "stop watching and quit?"
		noLabel = "keep watching"
		if !ctx.cfg.CloseMpvOnExit {
			msg = "quit? mpv will keep playing."
			noLabel = "stay"
		}
	}

	return push(newChoice("quit", msg, "quit", noLabel,
		func() tea.Cmd { return tea.Quit },
		nil,
	))
}

// ── Layout ────────────────────────────────────────────────────────────────────

// chromeLayout decides what fits.
//
// The body used to be floored at 3 rows, which meant View emitted more lines
// than the terminal had on a short window: the alt screen scrolled and the
// header disappeared off the top with no way back. Nothing can be floored —
// if the space isn't there, something has to go.
type chromeLayout struct {
	compact      bool // title only, no rules or blank line
	showRule     bool // divider above the footer
	showToast    bool
	showPlayer   bool
	showDownload bool
	body         int
}

func (c chromeLayout) headerLines() int {
	if c.compact {
		return 1
	}
	return 4 // rule, title, rule, blank
}

func (c chromeLayout) footerLines() int {
	n := 1 // key hints
	if c.showRule {
		n++
	}
	if c.showToast {
		n++
	}
	if c.showPlayer {
		n += 2 // now-playing line + progress bar
	}
	if c.showDownload {
		n++
	}
	return n
}

// chrome drops decoration until the body has at least one row, in order of
// what's least painful to lose.
func (a *app) chrome() chromeLayout {
	c := chromeLayout{
		showRule:     true,
		showToast:    a.toastText != "",
		showPlayer:   a.pstate.Alive,
		showDownload: ctx != nil && ctx.downloader != nil && ctx.downloader.Pending() > 0,
	}

	for range 5 {
		if a.h >= c.headerLines()+c.footerLines()+1 {
			break
		}
		switch {
		case c.showDownload:
			c.showDownload = false
		case c.showPlayer:
			c.showPlayer = false
		case c.showToast:
			c.showToast = false
		case !c.compact:
			c.compact = true
		case c.showRule:
			c.showRule = false
		}
	}

	c.body = max(1, a.h-c.headerLines()-c.footerLines())
	return c
}

func (a *app) bodyHeight() int { return a.chrome().body }

func (a *app) resize() {
	bh := a.bodyHeight()
	for _, s := range a.stack {
		s.SetSize(a.w, bh)
	}
}

func (a *app) View() string {
	if a.w == 0 {
		return "\n  starting…\n"
	}

	c := a.chrome()
	var b strings.Builder

	// Header
	crumbs := []string{stBrand.Render("stremio-cli") + stTitle.Render("uwu")}
	for _, s := range a.stack[1:] {
		if t := s.Title(); t != "" {
			crumbs = append(crumbs, stTitle.Render(t))
		}
	}
	title := " " + strings.Join(crumbs, stCrumb.Render(" › "))

	if c.compact {
		b.WriteString(clamp(title, a.w) + "\n")
	} else {
		bar := stRule.Render(strings.Repeat("▓", a.w))
		b.WriteString(bar + "\n" + clamp(title, a.w) + "\n" + bar + "\n\n")
	}

	// Body, padded to exactly c.body lines
	lines := strings.Split(a.top().View(), "\n")
	if len(lines) > c.body {
		lines = lines[:c.body]
	}
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString(strings.Repeat("\n", c.body-len(lines)+1))

	// Footer
	if c.showRule {
		b.WriteString(rule(a.w) + "\n")
	}

	hints := a.top().Footer()
	if a.pstate.Alive {
		hints += keyHintMore([2]string{"S", "subtitles"}, [2]string{"X", "stop mpv"})
	}

	// Last on the line, after the globals, so it sits in the same place on
	// every screen. The overlay itself says "? close" instead.
	if _, onKeys := a.top().(*keysScreen); !onKeys {
		hints += keyHintMore([2]string{"?", "keys"})
	}
	b.WriteString(clamp(hints, a.w))

	if c.showToast {
		st := stToast
		if a.toastErr {
			st = stErr
		}
		b.WriteString("\n " + clamp(st.Render(a.toastText), a.w-1))
	}

	if c.showDownload {
		b.WriteString("\n" + a.downloadBar())
	}
	if c.showPlayer {
		b.WriteString("\n" + a.playerBar())
	}
	return b.String()
}

// downloadBar is a single line: enough to know something's happening and
// roughly how far along, without leaving the screen you're on.
func (a *app) downloadBar() string {
	d, ok := ctx.downloader.Active()
	if !ok {
		return " " + stHint.Render(fmt.Sprintf("%d download(s) queued", ctx.downloader.Pending()))
	}

	right := fmt.Sprintf("%s / %s", format.Bytes(d.Done), format.Bytes(d.Total))
	if d.Speed > 0 {
		right += "  " + format.Bytes(d.Speed) + "/s"
	}
	if n := ctx.downloader.Pending(); n > 1 {
		right = fmt.Sprintf("+%d  ", n-1) + right
	}

	left := " " + stKey.Render("↓") + " " + stSelected.Render(d.Label)
	pad := a.w - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if pad < 1 {
		left = clamp(left, a.w-lipgloss.Width(right)-2)
		pad = max(1, a.w-lipgloss.Width(left)-lipgloss.Width(right)-1)
	}
	return left + strings.Repeat(" ", pad) + stSub.Render(right) + cReset
}

func (a *app) playerBar() string {
	s := a.pstate

	glyph := stBarFill.Render("▶")
	switch {
	case s.Loading:
		glyph = stWarn.Render("◌")
	case s.Buffering:
		glyph = stWarn.Render("⣾")
	case s.Paused:
		glyph = stWarn.Render("❚❚")
	}

	label := s.Label
	if label == "" {
		label = s.Title
	}

	right := format.ProgressGlyph(s.Pos, s.Duration)
	if s.QueueLen > 1 {
		right = stHint.Render(itoa(s.QueuePos)+"/"+itoa(s.QueueLen)) + "  " + right
	}
	if s.NextLabel != "" {
		right += "   " + stKey.Render("next: ") + stSub.Render(s.NextLabel)
	}

	left := " " + glyph + " " + stSelected.Render(label)
	pad := a.w - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if pad < 1 {
		left = clamp(left, a.w-lipgloss.Width(right)-2)
		pad = max(1, a.w-lipgloss.Width(left)-lipgloss.Width(right)-1)
	}

	line := left + strings.Repeat(" ", pad) + stSub.Render(right)
	return line + "\n" + progressBar(s.Frac(), a.w)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
