package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Vector from stremio-core's own docs for WatchedField.
func TestDecodeStremioCoreVector(t *testing.T) {
	anchor, length, bits, err := DecodeWatchedBitfield("tt2934286:1:5:5:eJyTZwAAAEAAIA==")
	if err != nil {
		t.Fatal(err)
	}
	if anchor != "tt2934286:1:5" || length != 5 {
		t.Errorf("anchor %q length %d", anchor, length)
	}
	for i := 0; i < 5; i++ {
		if !bits[i] {
			t.Errorf("bit %d should be set", i)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	vids := []string{"a", "b", "c", "d", "e"}
	enc := EncodeWatchedBitfield(vids, map[string]bool{"a": true, "c": true, "d": true})
	anchor, length, bits, err := DecodeWatchedBitfield(enc)
	if err != nil || anchor != "d" || length != 4 {
		t.Fatalf("%q %d %v", anchor, length, err)
	}
	if got := AlignedWatched(length, 3, 5, bits); !slices.Equal(got, []int{0, 2, 3}) {
		t.Errorf("got %v", got)
	}
}

// The anchor is stored at bit length-1; where it sits in the local list shifts
// every other bit with it.
func TestAlignedWatchedShiftsByAnchor(t *testing.T) {
	// Remote list is 100 long: episodes 1-3 and 48-50 watched, anchor is 50
	// (index 49, length 50).
	bits := map[int]bool{0: true, 1: true, 2: true, 47: true, 48: true, 49: true}

	// Local list knows only episodes 48..50, anchor at local index 2.
	// offset = 50-2-1 = 47, so local i reads stored bit i+47: all three set.
	if got := AlignedWatched(50, 2, 3, bits); !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("sparse tail: got %v", got)
	}

	// Same bitfield, local list of episodes 1..50 (anchor at index 49):
	// offset 0, so absolute indexing and the anchor-relative one agree.
	if got := AlignedWatched(50, 49, 50, bits); !slices.Equal(got, []int{0, 1, 2, 47, 48, 49}) {
		t.Errorf("full list: got %v", got)
	}

	if got := AlignedWatched(50, -1, 3, bits); got != nil {
		t.Errorf("missing anchor must mark nothing, got %v", got)
	}
}

func epsFor(sh *showState) map[string]bool {
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
	bf := EncodeWatchedBitfield(full, watched)

	sh := &showState{Eps: map[string]*epState{
		epKey(1, 48): {}, epKey(1, 49): {}, epKey(1, 50): {}, epKey(1, 51): {},
	}}
	h := &historyStore{}
	h.applyWatchedBitfield(sh, show, bf, 1000)

	got := epsFor(sh)
	if !got[epKey(1, 50)] {
		t.Error("episode 50 (the anchor) should be watched")
	}
	for _, e := range []int{48, 49, 51} {
		// Reading bit i as local video i would tick 48 and 49 here, from the
		// remote's episodes 1 and 2.
		if got[epKey(1, e)] {
			t.Errorf("episode %d was not watched remotely", e)
		}
	}
}

func TestWatchedBitfieldSparseSendsOnlyAnchor(t *testing.T) {
	const show = "tt2"
	cacheSeries.Clear()
	sh := &showState{Eps: map[string]*epState{
		epKey(1, 4):  {W: true},
		epKey(1, 9):  {W: true},
		epKey(1, 10): {},
	}}
	bf := watchedBitfield(show, sh)
	anchor, length, bits, err := DecodeWatchedBitfield(bf)
	if err != nil {
		t.Fatal(err)
	}
	if anchor != show+":1:9" || length != 1 || !bits[0] {
		t.Errorf("want anchor-only on E9, got %q len %d bits %v", anchor, length, bits)
	}
}

func TestWatchedBitfieldFullListKeepsAllBits(t *testing.T) {
	const show = "tt3"
	cacheSeries.Set(show, SeriesMeta{Videos: []Video{
		{Season: 1, Episode: 1}, {Season: 1, Episode: 2}, {Season: 1, Episode: 3}, {Season: 1, Episode: 4},
	}})
	t.Cleanup(cacheSeries.Clear)

	sh := &showState{Eps: map[string]*epState{epKey(1, 1): {W: true}, epKey(1, 3): {W: true}}}
	bf := watchedBitfield(show, sh)
	anchor, length, bits, err := DecodeWatchedBitfield(bf)
	if err != nil {
		t.Fatal(err)
	}
	if anchor != show+":1:3" || length != 3 || !bits[0] || bits[1] || !bits[2] {
		t.Errorf("got %q len %d bits %v", anchor, length, bits)
	}
}

func TestAnchorIndexRequiresShowPrefix(t *testing.T) {
	eps := [][2]int{{1, 1}, {1, 2}}
	if got := anchorIndex("tt1", "tt1:1:2", eps); got != 1 {
		t.Errorf("got %d", got)
	}
	for _, bad := range []string{"tt2:1:2", "tt1:1", "tt1:x:2", "undefined"} {
		if got := anchorIndex("tt1", bad, eps); got != -1 {
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
		s, e, ok := episodeOfVideoID(c.show, c.id)
		if ok != c.ok || (ok && (s != c.s || e != c.e)) {
			t.Errorf("%s %s: got %d %d %v", c.show, c.id, s, e, ok)
		}
	}
	// What we send must read back as what we meant.
	for _, show := range []string{"tt1", "kitsu:1234"} {
		s, e, ok := episodeOfVideoID(show, stremioVideoID(show, 1, 7))
		if !ok || s != 1 || e != 7 {
			t.Errorf("%s: round trip gave %d %d %v", show, s, e, ok)
		}
	}
	if got := stremioVideoID("kitsu:1234", 1, 5); got != "kitsu:1234:5" {
		t.Errorf("kitsu push id: %q", got)
	}
	if got := stremioVideoID("tt1", 1, 5); got != "tt1:1:5" {
		t.Errorf("imdb push id: %q", got)
	}
}

func TestSpecialsAreReportedAndFilmsAreNot(t *testing.T) {
	old := hist
	t.Cleanup(func() { hist = old })
	hist = &historyStore{loaded: true, byVideo: map[string]epRef{}, data: historyFile{
		Version: historyVer,
		Shows: map[string]*showState{"s": {Eps: map[string]*epState{
			epKey(0, 1): {W: true}, // special
			epKey(1, 1): {W: true},
			epKey(0, 0): {W: true}, // a film's key
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

func TestIsFilmEntry(t *testing.T) {
	if !isFilmEntry(HistoryEntry{}) {
		t.Error("no season, no episode is a film")
	}
	if isFilmEntry(HistoryEntry{Season: 0, Episode: 2}) {
		t.Error("season 0 episode 2 is a special, not a film")
	}
	if isFilmEntry(HistoryEntry{Season: 1, Episode: 1}) {
		t.Error("an ordinary episode is not a film")
	}
}

func TestFavItemUsesGivenHistory(t *testing.T) {
	recent := []HistoryEntry{{ID: "x", Season: 2, Episode: 3}}
	it := FavItem(Favourite{ID: "x", Name: "X", Type: "series"}, recent)
	if !strings.Contains(it.Badge, "S02E03") {
		t.Errorf("badge %q should show the last episode", it.Badge)
	}
}
