package player

import (
	"strings"
	"testing"

	"stremio-cliuwu/internal/stremio"
)

func TestSubMenuItems(t *testing.T) {
	subs := []stremio.Subtitle{
		{ID: "1", URL: "http://a/1", Lang: "eng", Addon: "OpenSubtitles"},
		{ID: "2", URL: "http://a/2", Lang: "fre", Addon: "Other"},
	}
	items := subMenuItems(subs, func(u string) bool { return u == "http://a/2" })
	if len(items) != 2 {
		t.Fatalf("items = %v, want 2", items)
	}
	if !strings.Contains(items[0], "English") || !strings.Contains(items[0], "OpenSubtitles") {
		t.Errorf("item 0 = %q, want language and addon", items[0])
	}
	if strings.Contains(items[0], "synced") {
		t.Errorf("item 0 = %q, must not say synced", items[0])
	}
	if !strings.Contains(items[1], "synced") {
		t.Errorf("item 1 = %q, want it marked synced", items[1])
	}
}

func TestSubMenuPick(t *testing.T) {
	m := &subMenu{id: 7, subs: []stremio.Subtitle{{ID: "a"}, {ID: "b"}}}
	cases := []struct {
		id, idx string
		want    string // "" = rejected
	}{
		{"7", "1", "a"},
		{"7", "2", "b"},
		{"7", "0", ""},  // 1-based
		{"7", "3", ""},  // past the end
		{"7", "-1", ""}, // negative
		{"7", "x", ""},  // not a number
		{"6", "1", ""},  // an older menu's answer
		{"", "", ""},
	}
	for _, c := range cases {
		got, ok := m.pick(c.id, c.idx)
		if (c.want == "") == ok || got.ID != c.want {
			t.Errorf("pick(%q,%q) = %q, %v; want %q", c.id, c.idx, got.ID, ok, c.want)
		}
	}
	var none *subMenu
	if _, ok := none.pick("1", "1"); ok {
		t.Error("pick on no menu succeeded")
	}
}
