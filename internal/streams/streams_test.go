package streams

import (
	"testing"

	"stremio-cliuwu/internal/stremio"
)

func TestSortStreamsAddonThenBinge(t *testing.T) {
	mk := func(url string, rank int, group string) stremio.Stream {
		s := stremio.Stream{URL: url, Rank: rank}
		s.BehaviorHints.BingeGroup = group
		return s
	}
	in := []stremio.Stream{mk("b-other", 1, ""), mk("a-other", 0, "x"), mk("a-binge", 0, "grp"), mk("b-binge", 1, "grp")}
	got := Sort(in, "", "grp", false)
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

func TestParseSizeUnits(t *testing.T) {
	cases := map[string]int64{
		"2 GB":    2_000_000_000,
		"1.5 GiB": int64(1.5 * (1 << 30)),
		"700 MB":  700_000_000,
		"no size": 0,
		"":        0,
	}
	for in, want := range cases {
		if got := ParseSize(in); got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestExtractResNormalises(t *testing.T) {
	cases := map[string]string{
		"Show.S01E01.2160p.WEB": "4K",
		"Show.1440p":            "2K",
		"Show.1080p.BluRay":     "1080P",
		"Show.NoResolution":     "",
	}
	for in, want := range cases {
		if got := ExtractRes(in); got != want {
			t.Errorf("ExtractRes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlockedMatchesWholeWordsAndPrefixes(t *testing.T) {
	mk := func(title string) stremio.Stream { return stremio.Stream{Title: title} }

	if Blocked(mk("anything"), nil) {
		t.Error("no terms must block nothing")
	}
	if !Blocked(mk("Show.S01.CAM.x264"), []string{"cam"}) {
		t.Error("a whole-word term should block")
	}
	if Blocked(mk("Show.S01.Camelot.x264"), []string{"cam"}) {
		t.Error("a whole-word term must not match inside another word")
	}
	if !Blocked(mk("Show.AI.Upscaled.1080p"), []string{"upscale*"}) {
		t.Error("a trailing * should match the start of a word")
	}
	if Blocked(mk("Show.1080p"), []string{"", "  "}) {
		t.Error("blank terms must be ignored")
	}
}

func mkStream(url, group string, cached *bool, name string) stremio.Stream {
	s := stremio.Stream{URL: url, Name: name, Cached: cached}
	s.BehaviorHints.BingeGroup = group
	return s
}

func TestAutoPick(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name    string
		in      []stremio.Stream
		binge   string
		blocked []string
		want    string // "" = nothing is safe to pick
	}{
		{"same release wins", []stremio.Stream{mkStream("a", "", &yes, ""), mkStream("b", "grp", nil, "")}, "grp", nil, "b"},
		{"same release but not cached: no guess", []stremio.Stream{mkStream("b", "grp", &no, "")}, "grp", nil, ""},
		{"cached link is safe", []stremio.Stream{mkStream("a", "", &yes, "")}, "", nil, "a"},
		{"library file is safe", []stremio.Stream{mkStream("a", "", nil, "📌 Library")}, "", nil, "a"},
		{"unknown, not same release: no guess", []stremio.Stream{mkStream("a", "", nil, "")}, "grp", nil, ""},
		{"not cached: no guess", []stremio.Stream{mkStream("a", "", &no, "")}, "", nil, ""},
		{"no url is not playable", []stremio.Stream{mkStream("", "grp", &yes, "")}, "grp", nil, ""},
		{"blocked term is skipped", []stremio.Stream{mkStream("a", "grp", &yes, "CAM rip"), mkStream("b", "", &yes, "web")}, "grp", []string{"cam"}, "b"},
		{"empty", nil, "grp", nil, ""},
	}
	for _, c := range cases {
		got, ok := AutoPick(c.in, "", c.binge, true, c.blocked)
		if (c.want == "") == ok || got.URL != c.want {
			t.Errorf("%s: AutoPick = %q, %v; want %q", c.name, got.URL, ok, c.want)
		}
	}
}
