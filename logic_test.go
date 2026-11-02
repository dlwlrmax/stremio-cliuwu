package main

import (
	"testing"
)

func TestPreferredLangsDedupesAliases(t *testing.T) {
	got := PreferredLangs("eng, en, English, spa")
	if len(got) != 2 || got[0] != "English" || got[1] != "Spanish" {
		t.Errorf("got %v", got)
	}
}

func TestPickPreferredHonoursOrder(t *testing.T) {
	subs := []Subtitle{{Lang: "spa", URL: "s"}, {Lang: "eng", URL: "e"}}
	if got := PickPreferred(subs, []string{"English"}); got == nil || got.URL != "e" {
		t.Errorf("got %+v", got)
	}
	if got := PickPreferred(subs, []string{"French"}); got != nil {
		t.Errorf("no match should be nil, got %+v", got)
	}
}

func TestMergeSubtitlesShippedFirstAndDeduped(t *testing.T) {
	shipped := []Subtitle{{Lang: "eng", URL: "ship"}}
	found := []Subtitle{{Lang: "eng", URL: "ship"}, {Lang: "eng", URL: "plugin", Rank: 1}, {Lang: "spa", URL: "es"}, {URL: ""}}
	got := MergeSubtitles(shipped, found, "eng")
	if len(got) != 3 {
		t.Fatalf("want 3 after dedupe and dropping empty URL, got %d: %+v", len(got), got)
	}
	if got[0].URL != "ship" || got[1].URL != "plugin" || got[2].URL != "es" {
		t.Errorf("order: %+v", got)
	}
}

func TestSortStreamsAddonThenBinge(t *testing.T) {
	mk := func(url string, rank int, group string) Stream {
		s := Stream{URL: url, Rank: rank}
		s.BehaviorHints.BingeGroup = group
		return s
	}
	in := []Stream{mk("b-other", 1, ""), mk("a-other", 0, "x"), mk("a-binge", 0, "grp"), mk("b-binge", 1, "grp")}
	got := SortStreams(in, "", "grp", false)
	want := []string{"a-binge", "a-other", "b-binge", "b-other"}
	for i, w := range want {
		if got[i].URL != w {
			t.Fatalf("pos %d: want %s, got %s (%v)", i, w, got[i].URL, urls(got))
		}
	}
}

func urls(ss []Stream) []string {
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
