package player

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"stremio-cliuwu/internal/langs"
	"stremio-cliuwu/internal/paths"
	"stremio-cliuwu/internal/stremio"
	"stremio-cliuwu/internal/subsync"
)

// The in-mpv subtitle menu. A script loaded into mpv (stremio-subs.lua) turns
// Alt+s into a client-message to us, we fetch the addons' subtitles and hand
// the list back as a menu, and the choice comes back the same way. mpv sends
// every script-message to its IPC clients, so no new transport is needed.

//go:embed stremio-subs.lua
var subsScript []byte

// scriptPath writes the embedded script where mpv can load it and returns the
// path, or "" if that isn't possible (mpv then runs without the menu).
func scriptPath() string {
	dir := filepath.Join(paths.ConfigDir(), "mpv")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, "stremio-subs.lua")
	if cur, err := os.ReadFile(path); err != nil || string(cur) != string(subsScript) {
		if err := os.WriteFile(path, subsScript, 0o644); err != nil {
			return ""
		}
	}
	return path
}

// subMenu is the list currently on screen in mpv.
type subMenu struct {
	id      int
	gen     uint64 // the file it was fetched for
	videoID string
	subs    []stremio.Subtitle
}

var menuSeq atomic.Int64

// subMenuItems is one line per subtitle. synced says whether a retimed copy
// of that URL exists, since choosing the row loads that copy.
func subMenuItems(subs []stremio.Subtitle, synced func(url string) bool) []string {
	out := make([]string, len(subs))
	for i, s := range subs {
		line := langs.Name(s.Lang) + " · " + s.Label()
		if s.Addon != "" {
			line += " · " + s.Addon
		}
		if synced != nil && synced(s.URL) {
			line += " · synced"
		}
		out[i] = line
	}
	return out
}

// pick resolves the script's answer (the menu id it was given and the 1-based
// row) to a subtitle. An answer for an older menu, or anything out of range,
// is refused.
func (m *subMenu) pick(id, index string) (stremio.Subtitle, bool) {
	if m == nil {
		return stremio.Subtitle{}, false
	}
	n, err := strconv.Atoi(id)
	if err != nil || n != m.id {
		return stremio.Subtitle{}, false
	}
	i, err := strconv.Atoi(index)
	if err != nil || i < 1 || i > len(m.subs) {
		return stremio.Subtitle{}, false
	}
	return m.subs[i-1], true
}

func (p *Player) onClientMessage(raw any) {
	args, _ := raw.([]any)
	var a []string
	for _, v := range args {
		s, _ := v.(string)
		a = append(a, s)
	}
	if len(a) == 0 {
		return
	}
	switch a[0] {
	case "stremio-cliuwu-subs-list":
		p.sendAsync(p.showSubMenu)
	case "stremio-cliuwu-subs-pick":
		if len(a) >= 3 {
			p.sendAsync(func() { p.pickFromMenu(a[1], a[2]) })
		}
	}
}

func (p *Player) osd(text string) {
	p.command("show-text", text, 3000)
}

// showSubMenu fetches subtitles for the playing file and sends mpv the menu.
func (p *Player) showSubMenu() {
	p.mu.Lock()
	now, cfg, gen := p.now, p.cfg, p.playGen
	p.mu.Unlock()
	if now == nil {
		p.osd("nothing is playing")
		return
	}
	req := *now
	req.Subs = append([]stremio.Subtitle(nil), now.Subs...)
	addons := append([]stremio.Addon(nil), p.currentAddons()...)

	subs := stremio.MergeSubtitles(req.Subs, stremio.GetSubtitles(addons, SubsQueryFrom(&req)), cfg.SubtitleLang)
	if !p.isCurrent(gen) {
		p.osd("") // the script's "fetching…" belongs to a file that's gone
		return
	}
	// With nothing from the addons the menu still opens, on the tracks alone.
	menu := &subMenu{id: int(menuSeq.Add(1)), gen: gen, videoID: req.VideoID, subs: subs}
	p.mu.Lock()
	p.menu = menu
	p.mu.Unlock()

	labels := subMenuItems(subs, func(url string) bool {
		_, ok := subsync.Recall(menu.videoID, url)
		return ok
	})
	type row struct {
		Label string `json:"label"`
		URL   string `json:"url"`  // lets the script spot a subtitle that is already a track
		File  string `json:"file"` // ...or one whose synced copy is
	}
	rows := make([]row, len(subs))
	for i, s := range subs {
		file, _ := subsync.Recall(menu.videoID, s.URL)
		rows[i] = row{Label: labels[i], URL: s.URL, File: file}
	}
	payload, _ := json.Marshal(map[string]any{"id": menu.id, "rows": rows})
	p.command("script-message-to", "stremio_subs", "menu", string(payload))
}

// pickFromMenu loads the row the user chose. A subtitle already synced for this
// video loads from its retimed copy.
func (p *Player) pickFromMenu(id, index string) {
	p.mu.Lock()
	menu := p.menu
	p.mu.Unlock()
	s, ok := menu.pick(id, index)
	if !ok || !p.isCurrent(menu.gen) {
		return
	}

	// A slow subtitle host can outlast an episode change, and sub-add then lands
	// on the new file; that is checked again below before anything is announced.
	var msg SubtitleAddedMsg
	if path, synced := subsync.Recall(menu.videoID, s.URL); synced {
		msg, _ = p.AddLocalSubtitle(path, langs.Name(s.Lang), s.Lang)().(SubtitleAddedMsg)
		msg.URL = s.URL // the list tracks rows by their original URL
	} else {
		msg, _ = p.AddSubtitle(s.URL, langs.Name(s.Lang), s.Lang)().(SubtitleAddedMsg)
	}
	if !p.isCurrent(menu.gen) {
		return
	}
	if msg.Err != nil {
		p.osd("couldn't load that subtitle — try another")
		return
	}
	p.emit(NoticeMsg{Text: "subtitles: " + langs.Name(s.Lang) + " · " + s.Label()})
	p.emit(msg)
}
