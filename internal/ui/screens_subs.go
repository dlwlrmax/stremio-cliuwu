package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"stremio-cliuwu/internal/langs"
	"stremio-cliuwu/internal/player"
	"stremio-cliuwu/internal/store"
	"stremio-cliuwu/internal/stremio"
	"stremio-cliuwu/internal/subsync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Subtitles are picked while something is playing, not while choosing a
// stream: you generally find out you want them a minute in, and by then the
// stream list is long gone.

type subsLoadedMsg struct {
	id   asyncID
	subs []stremio.Subtitle
}

// subSyncedMsg is the result of syncing one subtitle to the reference.
type subSyncedMsg struct {
	id   asyncID
	sub  stremio.Subtitle // the row that was synced
	path string           // the retimed file, inside the subsync temp dir
	err  error
}

type subsScreen struct {
	baseScreen
	id     asyncID
	query  stremio.SubsQuery
	label  string
	subs   []stremio.Subtitle
	shown  []int
	active string // url of the track currently loaded, for the marker

	// showID is the series this picker belongs to, or "" for a movie/"other".
	// It's what a manual pick is remembered under.
	showID  string
	pending *stremio.Subtitle // the track the user just picked, awaiting mpv's word

	list   listModel
	busy   busy
	loaded bool

	langs  []string // "all" plus each language present
	langIx int

	// Syncing: m marks a well-timed sub as the reference, S retimes the
	// highlighted one to it. The reference lives as long as this screen.
	ref        *stremio.Subtitle
	syncing    bool
	syncCancel context.CancelFunc
	// synced maps a retimed file's path back to the row it came from, so that
	// row is the one marked "on" once mpv has loaded the file.
	synced map[string]string
}

func newSubsScreen(label string, q stremio.SubsQuery, shipped []stremio.Subtitle, showID string) *subsScreen {
	l := newList()
	l.Empty = "no subtitles found"
	l.Numbered = true

	// Whatever came with the stream is already in hand, so it shows before
	// any request goes out.
	// Copied first: the same slice backs the play request, which the player
	// reads from another goroutine.
	shipped = slices.Clone(shipped)
	for i := range shipped {
		shipped[i].Addon = "stream"
	}

	return &subsScreen{
		id: newAsyncID(), query: q, label: label, subs: shipped, list: l,
		showID: showID,
		busy:   newBusy("looking for subtitles…"),
	}
}

func (s *subsScreen) Title() string { return "subtitles" }
func (s *subsScreen) Typing() bool  { return s.list.Typing() }

func (s *subsScreen) SetSize(w, h int) {
	s.baseScreen.SetSize(w, h)
	s.list.SetSize(w, h-2)
}

func (s *subsScreen) Init() tea.Cmd {
	id, q, addons := s.id, s.query, ctx.Addons()
	shipped := s.subs

	return tea.Batch(
		s.busy.start("looking for subtitles…"),
		func() tea.Msg {
			return subsLoadedMsg{
				id:   id,
				subs: stremio.MergeSubtitles(shipped, stremio.GetSubtitles(addons, q), ctx.cfg.SubtitleLang),
			}
		},
	)
}

func (s *subsScreen) Footer() string {
	pairs := [][2]string{{"enter", "try"}, {"m", "mark reference"}, {"S", "sync to it"}, {"0-9", "jump"}}
	if len(s.langs) > 2 {
		pairs = append(pairs, [2]string{"tab", "filter by language"})
	}
	if s.showID != "" {
		pairs = append(pairs, [2]string{"c", "use global"})
	}
	pairs = append(pairs, [2]string{"R", "refetch subtitles"}, [2]string{"/", "filter"},
		[2]string{"b/esc", "back"})
	return withStatus(s.list.Status(), keyHint(pairs...))
}

// collectLangs builds the language tabs, and opens on your preferred one.
//
// Grouped by canonical name rather than raw code, so "eng", "en" and
// "English" from three different addons are one tab instead of three.
func (s *subsScreen) collectLangs() {
	seen := map[string]bool{}
	s.langs = []string{"all"}

	for _, sub := range s.subs {
		if n := langs.Name(sub.Lang); !seen[n] {
			seen[n] = true
			s.langs = append(s.langs, n)
		}
	}

	// Land on the first of your preferred languages that actually came back.
	// The setting is a list, so "eng, en, English" means try each in turn.
	// A show-level preference wins over that: the picker opens where this show
	// was last set, so the common case needs no scrolling.
	s.langIx = 0
	want := ""
	if s.showID != "" {
		if p, ok := store.GetSubPref(s.showID); ok && p.Lang != store.SubOff {
			want = p.Lang
		}
	}
	if want == "" {
		if sub := stremio.PickPreferred(s.subs, langs.Preferred(ctx.cfg.SubtitleLang)); sub != nil {
			want = langs.Name(sub.Lang)
		}
	}
	for i, l := range s.langs {
		if i > 0 && l == want {
			s.langIx = i
			break
		}
	}
}

func (s *subsScreen) rebuild() {
	s.shown = s.shown[:0]

	filter := ""
	if s.langIx > 0 && s.langIx < len(s.langs) {
		filter = s.langs[s.langIx]
	}

	var items []Item
	for i, sub := range s.subs {
		if filter != "" && langs.Name(sub.Lang) != filter {
			continue
		}
		s.shown = append(s.shown, i)
		badge := grey(sub.Addon)
		if sub.URL == s.active {
			badge = good("✓ on") + "  " + badge
		}
		if s.ref != nil && sub.URL == s.ref.URL {
			badge = good("● ref") + "  " + badge
		}

		items = append(items, Item{
			Label: bold(langs.Name(sub.Lang)),
			Sub:   sub.Label(),
			Badge: badge,
		})
	}
	s.list.SetItems(items)
}

// langBar shows a window of tabs around the selected one.
//
// Twenty-odd languages don't fit on one line, and the whole row ran off the
// right edge — including, sometimes, the tab you were on. Windowing keeps the
// selection visible and marks that there's more either side.
func (s *subsScreen) langBar() string {
	if len(s.langs) < 3 {
		return ""
	}

	tab := func(i int) string {
		if i == s.langIx {
			return stCursor.Render(" " + s.langs[i] + " ")
		}
		return stHint.Render(" " + s.langs[i] + " ")
	}

	// Grow outwards from the selection until the line is full.
	budget := s.w - 8 // margins, plus room for the ellipses
	lo, hi := s.langIx, s.langIx
	used := lipgloss.Width(tab(s.langIx))

	for lo > 0 || hi < len(s.langs)-1 {
		grew := false
		if hi < len(s.langs)-1 {
			if w := lipgloss.Width(tab(hi+1)) + 1; used+w <= budget {
				hi, used, grew = hi+1, used+w, true
			}
		}
		if lo > 0 {
			if w := lipgloss.Width(tab(lo-1)) + 1; used+w <= budget {
				lo, used, grew = lo-1, used+w, true
			}
		}
		if !grew {
			break
		}
	}

	out := "  "
	if lo > 0 {
		out += stHint.Render("‹ ")
	}
	for i := lo; i <= hi; i++ {
		out += tab(i) + " "
	}
	if hi < len(s.langs)-1 {
		out += stHint.Render("›")
	}
	return out
}

func (s *subsScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case subsLoadedMsg:
		if m.id != s.id {
			return s, nil
		}
		s.busy.stop()
		s.loaded = true
		s.subs = m.subs
		s.collectLangs()
		s.rebuild()
		return s, nil

	case subSyncedMsg:
		if m.id != s.id {
			return s, nil
		}
		s.syncing = false
		s.syncCancel = nil
		if m.err != nil {
			switch {
			case errors.Is(m.err, subsync.ErrNoAlass):
				return s, toastErr("alass not found — install alass-cli")
			case errors.Is(m.err, context.Canceled):
				return s, nil
			}
			return s, toastErr("sync failed: " + m.err.Error())
		}
		if s.synced == nil {
			s.synced = map[string]string{}
		}
		s.synced[m.path] = m.sub.URL
		subsync.Remember(s.query.VideoID, subsync.Synced{URL: m.sub.URL, Lang: m.sub.Lang, Path: m.path})
		sub := m.sub
		s.pending = &sub
		return s, ctx.player.AddLocalSubtitle(m.path, langs.Name(m.sub.Lang), m.sub.Lang)

	case player.SubtitleAddedMsg:
		// A synced file arrives under its local path; everything below works
		// in terms of the row it came from.
		if origin, ok := s.synced[m.URL]; ok {
			m.URL = origin
		}
		if m.Err != nil {
			return s, toastErr("mpv couldn't load that one — try another")
		}
		// Remember only a track the user picked by hand. The autoloader emits
		// this message too, and its pick isn't a preference.
		if s.pending != nil && m.URL == s.pending.URL {
			src := "plugin"
			if s.pending.Addon == "stream" {
				src = "embedded"
			}
			store.SetSubPref(s.showID, langs.Name(s.pending.Lang), src)
			s.pending = nil

			// A hand pick is the end of the errand: close the list and get
			// back to the video. If playback was paused while the picker was
			// open, picking subtitles shouldn't leave it stuck that way.
			cmds := []tea.Cmd{toast("subtitles on — " + m.Title), pop()}
			if ctx.player.State().Paused {
				cmds = append(cmds, ctx.player.Resume())
			}
			return s, tea.Batch(cmds...)
		}

		// The autoloader's pick, not a preference — mark it and stay put.
		cur := s.list.Selected()
		s.active = m.URL
		s.rebuild()
		if cur >= 0 {
			s.list.Focus(cur)
		}
		return s, toast("subtitles on — " + m.Title)

	case tea.KeyMsg:
		if consumed, cmd := s.list.Update(msg); consumed {
			return s, cmd
		}
		switch m.String() {
		case "enter":
			i := s.list.Selected()
			if i < 0 || i >= len(s.shown) {
				return s, nil
			}
			s.list.ClearNum()
			sub := s.subs[s.shown[i]]
			s.pending = &sub

			// Synced earlier this session: that copy is the one with the
			// right timing, so it goes in instead of the original.
			if path, ok := subsync.Recall(s.query.VideoID, sub.URL); ok {
				if s.synced == nil {
					s.synced = map[string]string{}
				}
				s.synced[path] = sub.URL
				return s, tea.Batch(
					ctx.player.AddLocalSubtitle(path, langs.Name(sub.Lang), sub.Lang),
					toast("loading synced "+langs.Name(sub.Lang)+" subtitles…"),
				)
			}

			// Stays open until mpv confirms. The first pick often has the
			// wrong timing, so if it fails you're still on the list to try
			// the next one. A pick that takes closes the screen (see
			// SubtitleAddedMsg below).
			return s, tea.Batch(
				ctx.player.AddSubtitle(sub.URL, langs.Name(sub.Lang), sub.Lang),
				toast("loading "+langs.Name(sub.Lang)+" subtitles…"),
			)

		case "m":
			i := s.list.Selected()
			if i < 0 || i >= len(s.shown) {
				return s, nil
			}
			sub := s.subs[s.shown[i]]
			if s.ref != nil && s.ref.URL == sub.URL {
				s.ref = nil
			} else {
				s.ref = &sub
			}
			s.rebuild()
			s.list.Focus(i)
			return s, nil

		case "S":
			if s.syncing {
				return s, nil
			}
			i := s.list.Selected()
			if i < 0 || i >= len(s.shown) {
				return s, nil
			}
			sub := s.subs[s.shown[i]]
			if s.ref == nil {
				return s, toastErr("mark a well-timed sub first (m)")
			}
			if sub.URL == s.ref.URL {
				return s, toastErr("pick a different sub to sync")
			}
			cctx, cancel := context.WithCancel(context.Background())
			s.syncing, s.syncCancel = true, cancel
			id, ref := s.id, *s.ref
			run := func() tea.Msg {
				path, err := subsync.Sync(cctx, ref.URL, sub.URL)
				return subSyncedMsg{id: id, sub: sub, path: path, err: err}
			}
			return s, tea.Batch(toast("syncing…"), run)

		case "R":
			s.cancelSync()
			stremio.DropSubsCache(s.query)
			s.loaded = false
			s.id = newAsyncID()
			return s, s.Init()
		case "tab":
			if len(s.langs) > 1 {
				s.langIx = (s.langIx + 1) % len(s.langs)
				s.rebuild()
			}
		case "shift+tab":
			if len(s.langs) > 1 {
				s.langIx = (s.langIx - 1 + len(s.langs)) % len(s.langs)
				s.rebuild()
			}
		case "c":
			// Back to the global default for this show. Only meaningful when
			// a per-show preference could exist in the first place.
			if s.showID != "" {
				store.DeleteSubPref(s.showID)
				s.pending = nil
				return s, toast("subtitle preference cleared — using global")
			}
		case "esc", "backspace":
			s.cancelSync()
			return s, pop()
		case "q":
			s.cancelSync()
			return s, popRoot()
		}
	}
	return s, s.busy.update(msg)
}

// cancelSync stops a running sync, so leaving the screen doesn't leave alass
// working on a result nobody will look at.
func (s *subsScreen) cancelSync() {
	if s.syncCancel != nil {
		s.syncCancel()
		s.syncCancel = nil
	}
	s.syncing = false
}

func (s *subsScreen) View() string {
	if !s.loaded {
		return s.busy.view()
	}

	head := "  " + stSub.Render(s.label) + "\n"
	if bar := s.langBar(); bar != "" {
		head += bar + "\n"
	}
	if len(s.subs) == 0 {
		head += "\n" + stHint.Render(fmt.Sprintf(
			"  none of your addons returned subtitles for %s", s.query.VideoID))
	}
	return head + s.list.View()
}
