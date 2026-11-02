package main

import (
	"errors"
	"os"
	"path/filepath"
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

func TestCommonPrefixKeepsRunesWhole(t *testing.T) {
	// "é" and "è" share their first UTF-8 byte.
	if got := commonPrefix("caféa", "cafèb"); got != "caf" {
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

func TestReadJSONQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	os.WriteFile(p, []byte("{not json"), 0600)
	var v map[string]any
	if readJSON(p, &v) == nil {
		t.Fatal("expected a parse error")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("corrupt file should have been moved aside")
	}
	m, _ := filepath.Glob(p + ".corrupt-*")
	if len(m) != 1 {
		t.Errorf("want one quarantined copy, got %v", m)
	}
}

func TestOpenURLRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", `C:\Windows\calc.exe`, "javascript:alert(1)"} {
		if openURL(u) == nil {
			t.Errorf("%q should be refused", u)
		}
	}
}
