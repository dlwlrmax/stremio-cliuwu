package store

import (
	"os"
	"stremio-cliuwu/internal/paths"
	"testing"
	"time"
)

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
	SetSubPref("show", SubOff, "")
	LoadSubPrefs()
	if p, ok := GetSubPref("show"); !ok || p.Lang != SubOff {
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
	old := Hist
	t.Cleanup(func() { Hist = old })

	Hist = &HistoryStore{
		loaded:  true,
		pending: true, // keeps touch() from scheduling a background flush
		byVideo: map[string]EpRef{},
		data: HistoryFile{
			Version: HistoryVer,
			Shows: map[string]*ShowState{
				"keep": {Name: "Keep", Eps: map[string]*EpRecord{EpKey(1, 1): {}, EpKey(1, 2): {}}},
			},
			Recent: []RecentEntry{
				{ShowID: "orphan", Season: 1, Episode: 1}, // no matching show
				{ShowID: "keep", Season: 1, Episode: 1},
				{ShowID: "keep", Season: 1, Episode: 2},
			},
		},
	}

	items := Hist.recentEntries().Items
	if len(items) != 2 {
		t.Fatalf("orphan should be skipped, got %d items", len(items))
	}
	// items[0] is keep S1E1 but sits at data.Recent[1]; clearing "index 0"
	// the old way would have removed the orphan instead.
	ClearHistoryEntry(items[0])

	if _, ok := Hist.data.Shows["keep"].Eps[EpKey(1, 1)]; ok {
		t.Error("keep S1E1 state should be gone")
	}
	if _, ok := Hist.data.Shows["keep"].Eps[EpKey(1, 2)]; !ok {
		t.Error("keep S1E2 must survive")
	}
	if len(Hist.data.Recent) != 2 {
		t.Errorf("want 2 recent rows left, got %d", len(Hist.data.Recent))
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

// An existing config, from before the setting, gets autoplay on; once it has
// been migrated, turning it off sticks.
func TestAutoPlayNextDefaultsOnAndStaysOff(t *testing.T) {
	old := AppConfig{Version: 15}
	if !old.SetDefaults() || !old.AutoPlayNext {
		t.Fatalf("migrated config: AutoPlayNext = %v, want true", old.AutoPlayNext)
	}
	old.AutoPlayNext = false
	if old.SetDefaults(); old.AutoPlayNext {
		t.Error("SetDefaults turned autoplay back on after the user turned it off")
	}
}
