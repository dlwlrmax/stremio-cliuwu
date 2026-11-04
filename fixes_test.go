package main

import (
	"strings"
	"testing"
)

func TestMpvLangList(t *testing.T) {
	if got := mpvLangList(" eng, en , English,, "); got != "eng,en,English" {
		t.Errorf("got %q", got)
	}
}

func TestExpandPatternKeepsSlashInTitleOneSegment(t *testing.T) {
	got := expandPattern("{show}/Season {season}/{title}", map[string]string{
		"show": "Fate/Zero", "season": "1", "title": "x",
	})
	if len(got) != 3 || got[0] != "Fate-Zero" {
		t.Errorf("got %q", got)
	}
}

func TestSafeNameCapsLength(t *testing.T) {
	if n := len([]rune(safeName(strings.Repeat("a", 400)))); n > 120 {
		t.Errorf("length %d", n)
	}
}

func TestOpenURLRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", `C:\Windows\calc.exe`, "javascript:alert(1)"} {
		if openURL(u) == nil {
			t.Errorf("%q should be refused", u)
		}
	}
}
