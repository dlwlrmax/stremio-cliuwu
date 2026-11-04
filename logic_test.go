package main

import (
	"os"
	"stremio-cliuwu/internal/paths"
	"stremio-cliuwu/internal/stremio"
	"testing"
	"time"
)

func TestSortStreamsAddonThenBinge(t *testing.T) {
	mk := func(url string, rank int, group string) stremio.Stream {
		s := stremio.Stream{URL: url, Rank: rank}
		s.BehaviorHints.BingeGroup = group
		return s
	}
	in := []stremio.Stream{mk("b-other", 1, ""), mk("a-other", 0, "x"), mk("a-binge", 0, "grp"), mk("b-binge", 1, "grp")}
	got := SortStreams(in, "", "grp", false)
	want := []string{"a-binge", "a-other", "b-binge", "b-other"}
	for i, w := range want {
		if got[i].URL != w {
			t.Fatalf("pos %d: want %s, got %s (%v)", i, w, got[i].URL, urls(got))
		}
	}
}

func urls(ss []stremio.Stream) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.URL
	}
	return out
}

func TestSubPrefRoundTripAndOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	LoadSubPrefs()

	if _, ok := GetSubPref("show"); ok {
		t.Fatal("fresh store should be empty")
	}
	SetSubPref("show", "English", "plugin")
	if p, ok := GetSubPref("show"); !ok || p.Lang != "English" || p.Src != "plugin" {
		t.Errorf("got %+v %v", p, ok)
	}

	// "off" survives a reload from disk and is replaced by a hand pick.
	SetSubPref("show", subOff, "")
	LoadSubPrefs()
	if p, ok := GetSubPref("show"); !ok || p.Lang != subOff {
		t.Errorf("off not persisted: %+v %v", p, ok)
	}
	SetSubPref("show", "Spanish", "embedded")
	if p, _ := GetSubPref("show"); p.Lang != "Spanish" {
		t.Errorf("hand pick should replace off, got %+v", p)
	}

	DeleteSubPref("show")
	if _, ok := GetSubPref("show"); ok {
		t.Error("delete should forget the show")
	}
}

func TestBingeKeepsOldGroupWhenNewIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	LoadBingePrefs()
	SetBinge("show", "grpA")
	SetBinge("show", "")
	if got := GetBinge("show"); got != "grpA" {
		t.Errorf("empty group must not clear the choice, got %q", got)
	}
}

// A recent row whose show is gone is skipped by recentEntries, so the screen's
// index no longer lines up with data.Recent. Clearing has to hit the right row.
func TestClearHistoryEntryMatchesByIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := hist
	t.Cleanup(func() { hist = old })

	hist = &historyStore{
		loaded:  true,
		pending: true, // keeps touch() from scheduling a background flush
		byVideo: map[string]epRef{},
		data: historyFile{
			Version: historyVer,
			Shows: map[string]*showState{
				"keep": {Name: "Keep", Eps: map[string]*epState{epKey(1, 1): {}, epKey(1, 2): {}}},
			},
			Recent: []recentEntry{
				{ShowID: "orphan", Season: 1, Episode: 1}, // no matching show
				{ShowID: "keep", Season: 1, Episode: 1},
				{ShowID: "keep", Season: 1, Episode: 2},
			},
		},
	}

	items := hist.recentEntries().Items
	if len(items) != 2 {
		t.Fatalf("orphan should be skipped, got %d items", len(items))
	}
	// items[0] is keep S1E1 but sits at data.Recent[1]; clearing "index 0"
	// the old way would have removed the orphan instead.
	ClearHistoryEntry(items[0])

	if _, ok := hist.data.Shows["keep"].Eps[epKey(1, 1)]; ok {
		t.Error("keep S1E1 state should be gone")
	}
	if _, ok := hist.data.Shows["keep"].Eps[epKey(1, 2)]; !ok {
		t.Error("keep S1E2 must survive")
	}
	if len(hist.data.Recent) != 2 {
		t.Errorf("want 2 recent rows left, got %d", len(hist.data.Recent))
	}
}

// A hand pick in mpv's own menu is only worth remembering when it is a track
// inside the file: external ones are what our sub-add produced, and a track
// with no language tag could never be matched on the next episode.
func TestEmbeddedSubAt(t *testing.T) {
	tracks := []any{
		map[string]any{"type": "video", "id": float64(1)},
		map[string]any{"type": "sub", "id": float64(1), "lang": "eng", "title": "Full"},
		map[string]any{"type": "sub", "id": float64(2), "lang": "fre"},
		map[string]any{"type": "sub", "id": float64(3), "external": true, "lang": "vie"},
		map[string]any{"type": "sub", "id": float64(4)},
	}
	cases := []struct {
		sid         int
		lang, title string
		ok          bool
	}{
		{1, "English", "Full", true},
		{2, "French", "", true},
		{3, "", "", false}, // external: our own sub-add
		{4, "", "", false}, // no language tag
		{9, "", "", false}, // not in the list
	}
	for _, c := range cases {
		lang, title, ok := embeddedSubAt(tracks, c.sid)
		if lang != c.lang || title != c.title || ok != c.ok {
			t.Errorf("sid %d: got (%q, %q, %v), want (%q, %q, %v)", c.sid, lang, title, ok, c.lang, c.title, c.ok)
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

func TestEmbeddedPrefKeepsTrackAndSkipsNoChange(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	LoadSubPrefs()

	SetEmbeddedPref("show", "English", "Signs & Songs")
	LoadSubPrefs()
	p, ok := GetSubPref("show")
	if !ok || p.Lang != "English" || p.Src != "embedded" || p.Track != "Signs & Songs" {
		t.Fatalf("got %+v %v", p, ok)
	}

	// Saving the same thing again must not touch the file.
	fi1, _ := os.Stat(paths.SubsPrefsFile())
	time.Sleep(20 * time.Millisecond)
	SetEmbeddedPref("show", "English", "Signs & Songs")
	fi2, _ := os.Stat(paths.SubsPrefsFile())
	if !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Error("identical preference was rewritten")
	}

	// A plain pick of the language clears the old track title.
	SetSubPref("show", "English", "plugin")
	if p, _ := GetSubPref("show"); p.Track != "" {
		t.Errorf("track title should not outlive the pick, got %+v", p)
	}
}
