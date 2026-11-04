package format

import (
	"strings"
	"testing"
)

func TestCommonPrefixKeepsRunesWhole(t *testing.T) {
	// "é" and "è" share their first UTF-8 byte.
	if got := CommonPrefix("caféa", "cafèb"); got != "caf" {
		t.Errorf("got %q", got)
	}
}

func TestIsWatchedAtNotStarted(t *testing.T) {
	if IsWatchedAt(0, 10) {
		t.Error("position 0 must not count as watched")
	}
	if !IsWatchedAt(100, 100) {
		t.Error("end of file should count")
	}
}

func TestSecs(t *testing.T) {
	cases := map[float64]string{-5: "00:00", 0: "00:00", 59: "00:59", 61: "01:01", 3600: "1:00:00", 3725: "1:02:05"}
	for in, want := range cases {
		if got := Secs(in); got != want {
			t.Errorf("Secs(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestNaturalLessOrdersNumbersAsNumbers(t *testing.T) {
	if !NaturalLess("ep2", "ep10") || NaturalLess("ep10", "ep2") {
		t.Error("ep2 must sort before ep10")
	}
}

func TestVideoIDAndEp(t *testing.T) {
	if got := VideoID("tt1234:2:5"); got != "S02E05" {
		t.Errorf("VideoID = %q", got)
	}
	if got := Ep(2, 5, ""); got != "S02E05" {
		t.Errorf("Ep = %q", got)
	}
}

func TestCleanTitleDecodesAndDropsExtension(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"Show.Name.S01E02.1080p.mkv": "Show.Name.S01E02.1080p",
		"My%20Show%20-%2001.MP4":     "My Show - 01",
		"already clean":              "already clean",
		"double%2520encoded.mkv":     "double encoded",
	}
	for in, want := range cases {
		if got := CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandPatternKeepsSlashInTitleOneSegment(t *testing.T) {
	got := ExpandPattern("{show}/Season {season}/{title}", map[string]string{
		"show": "Fate/Zero", "season": "1", "title": "x",
	})
	if len(got) != 3 || got[0] != "Fate-Zero" {
		t.Errorf("got %q", got)
	}
}

func TestSafeNameCapsLength(t *testing.T) {
	if n := len([]rune(SafeName(strings.Repeat("a", 400)))); n > 120 {
		t.Errorf("length %d", n)
	}
}

func TestBytesUsesDecimalUnits(t *testing.T) {
	cases := map[int64]string{
		0:              "0 B",
		999:            "999 B",
		1500:           "1.5 KB",
		5_000_000:      "5.0 MB",
		37_300_000_000: "37.3 GB",
	}
	for in, want := range cases {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBaseName(t *testing.T) {
	cases := map[string]string{"a/b/c.mkv": "c.mkv", "c.mkv": "c.mkv", "dir/sub/": "sub", "": ""}
	for in, want := range cases {
		if got := BaseName(in); got != want {
			t.Errorf("BaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtFromURL(t *testing.T) {
	if got := ExtFromURL("https://x.test/a/b.mp4?token=1"); got != ".mp4" {
		t.Errorf("got %q", got)
	}
	if got := ExtFromURL("https://x.test/stream"); got != ".mkv" {
		t.Errorf("a URL with no extension should default to .mkv, got %q", got)
	}
}
