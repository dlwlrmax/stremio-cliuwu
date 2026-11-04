package store

import (
	"fmt"
	"stremio-cliuwu/internal/bitfield"
	"stremio-cliuwu/internal/stremio"
	"testing"
)

func epsFor(sh *ShowState) map[string]bool {
	out := map[string]bool{}
	for k, e := range sh.Eps {
		if e.W {
			out[k] = true
		}
	}
	return out
}

// The bug this guards: with only a few episodes known locally, bit 0 used to
// land on the first of them regardless of where the anchor really was. Here
// episodes 1-3 and 50 are watched remotely; the local list is 48..51.
func TestApplyWatchedBitfieldSparseStoreAligned(t *testing.T) {
	const show = "tt1"
	full := make([]string, 100)
	for i := range full {
		full[i] = fmt.Sprintf("%s:1:%d", show, i+1)
	}
	watched := map[string]bool{}
	for _, e := range []int{1, 2, 3, 50} {
		watched[full[e-1]] = true
	}
	bf := bitfield.Encode(full, watched)

	sh := &ShowState{Eps: map[string]*EpRecord{
		EpKey(1, 48): {}, EpKey(1, 49): {}, EpKey(1, 50): {}, EpKey(1, 51): {},
	}}
	h := &HistoryStore{}
	h.applyWatchedBitfield(sh, show, bf, 1000)

	got := epsFor(sh)
	if !got[EpKey(1, 50)] {
		t.Error("episode 50 (the anchor) should be watched")
	}
	for _, e := range []int{48, 49, 51} {
		// Reading bit i as local video i would tick 48 and 49 here, from the
		// remote's episodes 1 and 2.
		if got[EpKey(1, e)] {
			t.Errorf("episode %d was not watched remotely", e)
		}
	}
}

func TestWatchedBitfieldSparseSendsOnlyAnchor(t *testing.T) {
	const show = "tt2"
	stremio.CacheSeries.Clear()
	sh := &ShowState{Eps: map[string]*EpRecord{
		EpKey(1, 4):  {W: true},
		EpKey(1, 9):  {W: true},
		EpKey(1, 10): {},
	}}
	bf := WatchedBitfield(show, sh)
	anchor, length, bits, err := bitfield.Decode(bf)
	if err != nil {
		t.Fatal(err)
	}
	if anchor != show+":1:9" || length != 1 || !bits[0] {
		t.Errorf("want anchor-only on E9, got %q len %d bits %v", anchor, length, bits)
	}
}

func TestWatchedBitfieldFullListKeepsAllBits(t *testing.T) {
	const show = "tt3"
	stremio.CacheSeries.Set(show, stremio.SeriesMeta{Videos: []stremio.Video{
		{Season: 1, Episode: 1}, {Season: 1, Episode: 2}, {Season: 1, Episode: 3}, {Season: 1, Episode: 4},
	}})
	t.Cleanup(stremio.CacheSeries.Clear)

	sh := &ShowState{Eps: map[string]*EpRecord{EpKey(1, 1): {W: true}, EpKey(1, 3): {W: true}}}
	bf := WatchedBitfield(show, sh)
	anchor, length, bits, err := bitfield.Decode(bf)
	if err != nil {
		t.Fatal(err)
	}
	if anchor != show+":1:3" || length != 3 || !bits[0] || bits[1] || !bits[2] {
		t.Errorf("got %q len %d bits %v", anchor, length, bits)
	}
}

func TestAnchorIndexRequiresShowPrefix(t *testing.T) {
	eps := [][2]int{{1, 1}, {1, 2}}
	if got := AnchorIndex("tt1", "tt1:1:2", eps); got != 1 {
		t.Errorf("got %d", got)
	}
	for _, bad := range []string{"tt2:1:2", "tt1:1", "tt1:x:2", "undefined"} {
		if got := AnchorIndex("tt1", bad, eps); got != -1 {
			t.Errorf("%q: got %d", bad, got)
		}
	}
}

func TestEpisodeIDsKitsuAndImdb(t *testing.T) {
	cases := []struct {
		show, id string
		s, e     int
		ok       bool
	}{
		{"tt1", "tt1:2:5", 2, 5, true},
		{"tt1", "tt1:0:3", 0, 3, true}, // specials
		{"kitsu:1234", "kitsu:1234:5", 1, 5, true},
		{"kitsu:1234", "kitsu:1234:2:5", 2, 5, true},
		{"tt1", "tt1:5", 0, 0, false},
		{"tt1", "undefined", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := EpisodeOfVideoID(c.show, c.id)
		if ok != c.ok || (ok && (s != c.s || e != c.e)) {
			t.Errorf("%s %s: got %d %d %v", c.show, c.id, s, e, ok)
		}
	}
	// What we send must read back as what we meant.
	for _, show := range []string{"tt1", "kitsu:1234"} {
		s, e, ok := EpisodeOfVideoID(show, StremioVideoID(show, 1, 7))
		if !ok || s != 1 || e != 7 {
			t.Errorf("%s: round trip gave %d %d %v", show, s, e, ok)
		}
	}
	if got := StremioVideoID("kitsu:1234", 1, 5); got != "kitsu:1234:5" {
		t.Errorf("kitsu push id: %q", got)
	}
	if got := StremioVideoID("tt1", 1, 5); got != "tt1:1:5" {
		t.Errorf("imdb push id: %q", got)
	}
}

func TestSpecialsAreReportedAndFilmsAreNot(t *testing.T) {
	old := Hist
	t.Cleanup(func() { Hist = old })
	Hist = &HistoryStore{loaded: true, byVideo: map[string]EpRef{}, data: HistoryFile{
		Version: HistoryVer,
		Shows: map[string]*ShowState{"s": {Eps: map[string]*EpRecord{
			EpKey(0, 1): {W: true}, // special
			EpKey(1, 1): {W: true},
			EpKey(0, 0): {W: true}, // a film's key
		}}},
	}}
	got := EpisodeStates("s")
	if !got[[2]int{0, 1}].Watched {
		t.Error("season 0 episode should be reported")
	}
	if _, ok := got[[2]int{0, 0}]; ok {
		t.Error("a film's 0:0 is not an episode")
	}
	if len(got) != 2 {
		t.Errorf("got %v", got)
	}
}
