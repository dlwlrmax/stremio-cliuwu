package main

import (
	"testing"
)

func TestMpvLangList(t *testing.T) {
	if got := mpvLangList(" eng, en , English,, "); got != "eng,en,English" {
		t.Errorf("got %q", got)
	}
}

func TestOpenURLRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", `C:\Windows\calc.exe`, "javascript:alert(1)"} {
		if openURL(u) == nil {
			t.Errorf("%q should be refused", u)
		}
	}
}
