package player

import (
	"testing"

	"stremio-cliuwu/internal/store"
)

// A hand-picked subtitle track is remembered by language, and by title when
// it is inside the file. An external one is an addon subtitle already loaded,
// chosen again from the track rows. A track with no language tag could never
// be matched on the next episode.
func TestSubPickPref(t *testing.T) {
	raw := []any{
		map[string]any{"type": "video", "id": float64(1)},
		map[string]any{"type": "sub", "id": float64(1), "lang": "eng", "title": "Full"},
		map[string]any{"type": "sub", "id": float64(2), "lang": "fre"},
		map[string]any{"type": "sub", "id": float64(3), "external": true, "lang": "vie", "title": "Vietnamese"},
		map[string]any{"type": "sub", "id": float64(4)},
	}
	subs := tracks(raw, "sub")
	cases := []struct {
		sid  int
		want store.ShowSubPref
		ok   bool
	}{
		{1, store.ShowSubPref{Lang: "English", Src: "embedded", Track: "Full"}, true},
		{2, store.ShowSubPref{Lang: "French", Src: "embedded"}, true},
		{3, store.ShowSubPref{Lang: "Vietnamese", Src: "plugin"}, true}, // external: title is ours, not the file's
		{4, store.ShowSubPref{}, false},                                 // no language tag
		{9, store.ShowSubPref{}, false},                                 // not in the list
	}
	for _, c := range cases {
		var got store.ShowSubPref
		tr, ok := trackAt(subs, c.sid)
		if ok {
			got, ok = subPickPref(tr)
		}
		if got != c.want || ok != c.ok {
			t.Errorf("sid %d: got (%+v, %v), want (%+v, %v)", c.sid, got, ok, c.want, c.ok)
		}
	}
}

// A file often has several tracks in one language (full, SDH, signs only), so
// the remembered title picks between them, and an external track with the same
// language is never an "in-file" match.
func TestPickEmbeddedSub(t *testing.T) {
	tracks := []any{
		map[string]any{"type": "sub", "id": float64(1), "lang": "eng", "title": "Signs & Songs"},
		map[string]any{"type": "sub", "id": float64(2), "lang": "eng", "title": "Full", "selected": true},
		map[string]any{"type": "sub", "id": float64(3), "lang": "eng", "external": true},
		map[string]any{"type": "sub", "id": float64(4), "lang": "fre"},
	}
	cases := []struct {
		name, lang, title string
		id                int
		selected, ok      bool
	}{
		{"title picks among same language", "English", "Signs & Songs", 1, false, true},
		{"already active is reported", "English", "Full", 2, true, true},
		{"unknown title falls back to first", "English", "Missing", 1, false, true},
		{"no title takes first", "English", "", 1, false, true},
		{"other language", "French", "", 4, false, true},
		{"absent language", "Spanish", "", 0, false, false},
	}
	for _, c := range cases {
		id, sel, ok := pickEmbeddedSub(tracks, c.lang, c.title)
		if id != c.id || sel != c.selected || ok != c.ok {
			t.Errorf("%s: got (%d, %v, %v), want (%d, %v, %v)", c.name, id, sel, ok, c.id, c.selected, c.ok)
		}
	}

	// Only an external track has the language: nothing in the file.
	only := []any{map[string]any{"type": "sub", "id": float64(3), "lang": "eng", "external": true}}
	if _, _, ok := pickEmbeddedSub(only, "English", ""); ok {
		t.Error("an external track must not count as in-file")
	}
}

func TestMpvLangList(t *testing.T) {
	if got := mpvLangList(" eng, en , English,, "); got != "eng,en,English" {
		t.Errorf("got %q", got)
	}
}

// A synced subtitle is a local file, and mpv will open whatever path it is
// given. Only files the app generated into its own temp directory may go in:
// a path from anywhere else is refused before mpv hears about it.
func TestAddLocalSubtitleRejectsForeignPath(t *testing.T) {
	p := &Player{} // rejection happens before any IPC, so no mpv is needed
	for _, path := range []string{"/etc/passwd", "../x.srt", "x.srt", ""} {
		msg, ok := p.AddLocalSubtitle(path, "x", "eng")().(SubtitleAddedMsg)
		if !ok {
			t.Fatalf("%q: want a SubtitleAddedMsg", path)
		}
		if msg.Err == nil {
			t.Errorf("%q: accepted, want an error", path)
		}
		if msg.URL != path {
			t.Errorf("%q: msg.URL = %q, want the path echoed back", path, msg.URL)
		}
	}
}

// The audio autoload ranks the show's remembered language first, breaks ties
// in that language on the remembered title, and reads tracks the way mpv
// reports them.
func TestAudioRanking(t *testing.T) {
	raw := []any{
		map[string]any{"type": "video", "id": float64(1)},
		map[string]any{"type": "audio", "id": float64(1), "lang": "jpn", "default": true, "selected": true},
		map[string]any{"type": "audio", "id": float64(2), "lang": "eng", "title": "Stereo"},
		map[string]any{"type": "audio", "id": float64(3), "lang": "eng", "title": "Commentary"},
		map[string]any{"type": "audio", "id": float64(4), "lang": "  "},
	}
	ts := tracks(raw, "audio")
	if len(ts) != 4 || ts[0].lang != "Japanese" || !ts[0].selected || !ts[0].def || ts[3].lang != "" {
		t.Fatalf("tracks: %+v", ts)
	}
	pick := func(prefs []string, pref store.ShowAudioPref) int {
		best := ts[0]
		for _, tr := range ts[1:] {
			if audioBetter(tr, best, prefs, pref) {
				best = tr
			}
		}
		return best.id
	}
	cases := []struct {
		prefs []string
		pref  store.ShowAudioPref
		want  int
	}{
		{[]string{"Japanese", "English"}, store.ShowAudioPref{}, 1},
		{[]string{"English", "Japanese"}, store.ShowAudioPref{}, 2}, // first English by id
		{[]string{"English", "Japanese"}, store.ShowAudioPref{Lang: "English", Track: "Commentary"}, 3},
		{[]string{"German"}, store.ShowAudioPref{}, 1}, // nothing listed: default, then id
		// A title remembered for another language must not steer this one.
		{[]string{"French"}, store.ShowAudioPref{Lang: "French", Track: "Commentary"}, 1},
	}
	for _, c := range cases {
		if got := pick(c.prefs, c.pref); got != c.want {
			t.Errorf("prefs=%v pref=%+v: got %d want %d", c.prefs, c.pref, got, c.want)
		}
	}
}
