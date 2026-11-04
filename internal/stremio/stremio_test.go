package stremio

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRedactErrHidesKeys(t *testing.T) {
	err := errors.New(`Get "https://torrentio.strem.fun/realdebrid=ABCDEFGHIJKLMNOP/manifest.json?token=SECRETSECRET": EOF`)
	got := RedactErr(err)
	for _, secret := range []string{"ABCDEFGHIJKLMNOP", "SECRETSECRET"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q leaked in %q", secret, got)
		}
	}
	if !strings.Contains(got, "torrentio.strem.fun") {
		t.Errorf("host should stay readable: %q", got)
	}
}

func TestRedactURLShortAndOpaque(t *testing.T) {
	if got := RedactURL("https://x.test/key=abc123/manifest.json"); strings.Contains(got, "abc123") {
		t.Errorf("short value leaked: %q", got)
	}
	blob := "eyJwcm92aWRlciI6InJkIiwia2V5IjoiWFhYIn0"
	if got := RedactURL("https://x.test/" + blob + "/manifest.json"); strings.Contains(got, blob) {
		t.Errorf("opaque config segment leaked: %q", got)
	}
	if got := RedactURL("https://user:pw@x.test/manifest.json"); strings.Contains(got, "pw") {
		t.Errorf("userinfo leaked: %q", got)
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

func TestNormalizeManifestURL(t *testing.T) {
	cases := map[string]string{
		"https://x.test/manifest.json":      "https://x.test/manifest.json",
		"x.test":                            "https://x.test/manifest.json",
		"stremio://x.test/addon/":           "https://x.test/addon/manifest.json",
		"[Addon](https://x.test/a)":         "https://x.test/a/manifest.json",
		"  http://x.test/a/manifest.json  ": "http://x.test/a/manifest.json",
	}
	for in, want := range cases {
		got, err := NormalizeManifestURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeManifestURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "https://"} {
		if _, err := NormalizeManifestURL(bad); err == nil {
			t.Errorf("NormalizeManifestURL(%q) should fail", bad)
		}
	}
}

func TestSupportsResourceChecksTypeAndIDPrefix(t *testing.T) {
	a := Addon{Manifest: Manifest{Resources: []any{
		"catalog", // plain string: no restrictions
		map[string]any{"name": "stream", "types": []any{"series"}, "idPrefixes": []any{"tt", "kitsu:"}},
		map[string]any{"name": "subtitles"},
	}}}

	cases := []struct {
		name, kind, id string
		want           bool
	}{
		{"catalog", "movie", "anything", true},
		{"stream", "series", "tt123", true},
		{"stream", "series", "kitsu:9", true},
		{"stream", "series", "tmdb:5", false}, // prefix not served
		{"stream", "movie", "tt123", false},   // type not served
		{"subtitles", "movie", "tmdb:5", true},
		{"meta", "movie", "tt1", false}, // not declared at all
	}
	for _, c := range cases {
		if got := a.SupportsResource(c.name, c.kind, c.id); got != c.want {
			t.Errorf("SupportsResource(%q,%q,%q) = %v, want %v", c.name, c.kind, c.id, got, c.want)
		}
	}
	if !a.HasStreams() {
		t.Error("an addon declaring stream should report HasStreams")
	}
}

func TestFlexStringAcceptsNumbersAndShapes(t *testing.T) {
	var v struct {
		A FlexString `json:"a"`
		B FlexString `json:"b"`
		C FlexString `json:"c"`
		D FlexString `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"a":"x","b":8.5,"c":["k"],"d":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "x" || v.B != "8.5" || v.C != "" || v.D != "" {
		t.Errorf("got %+v", v)
	}
}
