package subsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const srtBody = "1\n00:00:01,000 --> 00:00:02,000\nhi\n"
const vttBody = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nhi\n"

// fakeAlass installs a shell script as alass-cli and points lookPath at it.
func fakeAlass(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "alass-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := lookPath
	lookPath = func(name string) (string, error) {
		if name == "alass-cli" {
			return bin, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = old })
}

func server(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range routes {
		body := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSyncSuccess(t *testing.T) {
	t.Cleanup(Cleanup)
	fakeAlass(t, `cp "$2" "$3"`)
	srv := server(t, map[string]string{"/ref.srt": srtBody, "/sub.vtt": vttBody})

	out, err := Sync(context.Background(), srv.URL+"/ref.srt", srv.URL+"/sub.vtt")
	if err != nil {
		t.Fatal(err)
	}
	if !Owns(out) {
		t.Errorf("Owns(%q) = false, want true", out)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "00:00:01,000 --> 00:00:02,000") {
		t.Errorf("alass saw unconverted WebVTT, output:\n%s", got)
	}
	// Only the result is kept; the downloaded inputs are gone.
	entries, _ := os.ReadDir(filepath.Dir(out))
	if len(entries) != 1 || entries[0].Name() != "out.srt" {
		t.Errorf("want only out.srt left behind, got %v", entries)
	}
}

func TestSyncNoAlass(t *testing.T) {
	t.Cleanup(Cleanup)
	old := lookPath
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPath = old })
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})

	_, err := Sync(context.Background(), srv.URL+"/a", srv.URL+"/b")
	if !errors.Is(err, ErrNoAlass) {
		t.Errorf("err = %v, want ErrNoAlass", err)
	}
}

func TestSyncTooLarge(t *testing.T) {
	t.Cleanup(Cleanup)
	marker := filepath.Join(t.TempDir(), "ran")
	fakeAlass(t, "touch "+marker)
	srv := server(t, map[string]string{
		"/ref":   srtBody,
		"/big":   strings.Repeat("x", 4<<20+1),
		"/exact": strings.Repeat("x", 4<<20),
	})

	_, err := Sync(context.Background(), srv.URL+"/ref", srv.URL+"/big")
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("alass ran even though a body was too large")
	}
	// Exactly at the cap is allowed through to alass.
	if _, err := Sync(context.Background(), srv.URL+"/ref", srv.URL+"/exact"); errors.Is(err, ErrTooLarge) {
		t.Error("a body of exactly 4 MB must not be ErrTooLarge")
	}
}

func TestSyncAlassErrorOnStdout(t *testing.T) {
	t.Cleanup(Cleanup)
	fakeAlass(t, `printf "error: parsing failed\ncaused by: bad digit\n\nnot: run with environment variable 'RUST_BACKTRACE=1'\n"; exit 1`)
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})

	_, err := Sync(context.Background(), srv.URL+"/a", srv.URL+"/b")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "caused by: bad digit") {
		t.Errorf("error %q does not carry alass's reason", err)
	}
	if strings.Contains(err.Error(), "RUST_BACKTRACE") {
		t.Errorf("error %q still has the backtrace hint", err)
	}
}

func TestSyncHTTPStatus(t *testing.T) {
	t.Cleanup(Cleanup)
	fakeAlass(t, `cp "$2" "$3"`)
	srv := server(t, map[string]string{"/ok": srtBody})

	_, err := Sync(context.Background(), srv.URL+"/ok", srv.URL+"/missing")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want it to mention 404", err)
	}
}

func TestSyncCancelled(t *testing.T) {
	t.Cleanup(Cleanup)
	fakeAlass(t, "exec sleep 30")
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() { _, err := Sync(ctx, srv.URL+"/a", srv.URL+"/b"); done <- err }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled in the chain", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Sync did not return after cancel")
	}
}

func TestOwns(t *testing.T) {
	Cleanup()
	t.Cleanup(Cleanup)
	if Owns("/etc/passwd") {
		t.Error("Owns is true before any temp directory exists")
	}

	fakeAlass(t, `cp "$2" "$3"`)
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})
	out, err := Sync(context.Background(), srv.URL+"/a", srv.URL+"/b")
	if err != nil {
		t.Fatal(err)
	}
	if !Owns(out) {
		t.Fatalf("Owns(%q) = false", out)
	}

	dir := filepath.Dir(out)
	if err := os.Symlink("/etc", filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "filelink")); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"dot-dot escape":       dir + "/../../x", // dir is <root>/s-N, so this is outside <root>
		"outside absolute":     "/etc/passwd",
		"relative":             "../x.srt",
		"through dir symlink":  filepath.Join(dir, "dirlink", "passwd"),
		"file symlink":         filepath.Join(dir, "filelink"),
		"the directory itself": filepath.Dir(dir),
	} {
		if Owns(path) {
			t.Errorf("%s: Owns(%q) = true, want false", name, path)
		}
	}
}

func TestCleanup(t *testing.T) {
	fakeAlass(t, `cp "$2" "$3"`)
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})
	out, err := Sync(context.Background(), srv.URL+"/a", srv.URL+"/b")
	if err != nil {
		t.Fatal(err)
	}
	Cleanup()
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("output still exists after Cleanup: %v", err)
	}
	Cleanup() // a second call, and one with nothing created, must not panic
}

// The fakes above can't tell whether the real tool is called correctly, so
// this one runs it. Skipped where alass-cli isn't installed.
func TestSyncRealAlass(t *testing.T) {
	if _, err := exec.LookPath("alass-cli"); err != nil {
		t.Skip("alass-cli not installed")
	}
	t.Cleanup(Cleanup)

	// Irregular spacing, so there is only one alignment that fits.
	gaps := []int{3, 7, 2, 11, 4, 9, 5, 13, 2, 8, 6, 10, 3, 12, 4, 7, 9, 5, 14, 3}
	build := func(offsetMs int, vtt bool) string {
		var b strings.Builder
		if vtt {
			b.WriteString("WEBVTT\n\n")
		}
		at := 10_000
		for i, g := range gaps {
			at += g * 1000
			ts := func(ms int) string {
				sep := ","
				if vtt {
					sep = "."
				}
				return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3600000, ms/60000%60, ms/1000%60, sep, ms%1000)
			}
			if !vtt {
				fmt.Fprintf(&b, "%d\n", i+1)
			}
			fmt.Fprintf(&b, "%s --> %s\nline %d\n\n", ts(at+offsetMs), ts(at+offsetMs+1500), i)
		}
		return b.String()
	}
	// The subtitle is WebVTT, shifted 6 s late, and served under a .srt name.
	srv := server(t, map[string]string{"/ref.srt": build(0, false), "/sub.srt": build(6000, true)})

	out, err := Sync(context.Background(), srv.URL+"/ref.srt", srv.URL+"/sub.srt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if !strings.Contains(string(got), "00:00:13,000 --> 00:00:14,500") {
		t.Errorf("first cue not pulled back to the reference timing (13.000 s):\n%.200s", got)
	}
}

// A host that answers 200 with a web page or nothing is the likely way a
// download goes wrong, and alass's own message for it ("invalid digit found in
// string") says nothing useful. These are refused before alass runs.
func TestSyncRefusesNonSubtitleBodies(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"html page", "<!DOCTYPE html><html><body>Rate limited</body></html>", "web page"},
		{"html page after whitespace and BOM", "\xef\xbb\xbf  \n<html></html>", "web page"},
		{"empty", "", "empty"},
		{"whitespace only", " \r\n\t\n", "empty"},
		{"webvtt with no cues", "WEBVTT\n\nNOTE only a comment\n", "no cues"},
	}
	for _, c := range cases {
		for _, side := range []string{"ref", "sub"} {
			t.Run(c.name+"/"+side, func(t *testing.T) {
				t.Cleanup(Cleanup)
				marker := filepath.Join(t.TempDir(), "ran")
				fakeAlass(t, "touch "+marker)
				srv := server(t, map[string]string{"/good": srtBody, "/bad": c.body})
				refURL, subURL := srv.URL+"/good", srv.URL+"/bad"
				wantSide := "subtitle:"
				if side == "ref" {
					refURL, subURL = subURL, refURL
					wantSide = "reference subtitle:"
				}

				_, err := Sync(context.Background(), refURL, subURL)
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v, want it to contain %q", err, c.want)
				}
				if !strings.Contains(err.Error(), wantSide) {
					t.Errorf("err = %v, want it to say which file was bad (%q)", err, wantSide)
				}
				if _, statErr := os.Stat(marker); statErr == nil {
					t.Error("alass ran on a body that is not a subtitle")
				}
			})
		}
	}
}

// alass prints a chain of "caused by:" lines, innermost last. The innermost on
// its own ("invalid digit found in string") is the least informative; the line
// above it says what was expected and what was found.
func TestSyncAlassErrorKeepsTheChain(t *testing.T) {
	t.Cleanup(Cleanup)
	fakeAlass(t, `printf "error: parsing subtitle file '/tmp/x/sub.srt' failed\ncaused by: parsing the subtitle data failed\ncaused by: expected SubRip index line, found 'Hello'\ncaused by: invalid digit found in string\n\nnot: run with environment variable 'RUST_BACKTRACE=1'\n"; exit 1`)
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})

	_, err := Sync(context.Background(), srv.URL+"/a", srv.URL+"/b")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"expected SubRip index line, found 'Hello'", "invalid digit found in string"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "RUST_BACKTRACE") {
		t.Errorf("error %q still has the backtrace hint", err)
	}
}

func TestRememberRecall(t *testing.T) {
	Cleanup()
	t.Cleanup(Cleanup)
	if _, ok := Recall("v", "u"); ok {
		t.Fatal("Recall found something nobody remembered")
	}

	fakeAlass(t, `cp "$2" "$3"`)
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})
	out, err := Sync(context.Background(), srv.URL+"/a", srv.URL+"/b")
	if err != nil {
		t.Fatal(err)
	}
	Remember("v", Synced{URL: "u", Lang: "eng", Path: out})
	if got, ok := Recall("v", "u"); !ok || got != out {
		t.Fatalf("Recall = %q, %v; want %q, true", got, ok, out)
	}
	if _, ok := Recall("other video", "u"); ok {
		t.Error("Recall crossed videos")
	}

	// A path the package doesn't own is never remembered.
	Remember("v", Synced{URL: "evil", Lang: "eng", Path: "/etc/passwd"})
	if _, ok := Recall("v", "evil"); ok {
		t.Error("Recall returned a path outside the temp directory")
	}

	// The file going away (or Cleanup) means there is nothing to load.
	os.Remove(out)
	if _, ok := Recall("v", "u"); ok {
		t.Error("Recall returned a file that no longer exists")
	}
}

func TestLatest(t *testing.T) {
	Cleanup()
	t.Cleanup(Cleanup)
	fakeAlass(t, `cp "$2" "$3"`)
	srv := server(t, map[string]string{"/a": srtBody, "/b": srtBody})
	mk := func() string {
		out, err := Sync(context.Background(), srv.URL+"/a", srv.URL+"/b")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	eng1, fre, eng2 := mk(), mk(), mk()
	Remember("v", Synced{URL: "e1", Lang: "eng", Path: eng1})
	Remember("v", Synced{URL: "f", Lang: "fre", Path: fre})
	Remember("v", Synced{URL: "e2", Lang: "eng", Path: eng2})

	isEng := func(l string) bool { return l == "eng" }
	if got, ok := Latest("v", isEng); !ok || got.URL != "e2" {
		t.Errorf("Latest(eng) = %+v, %v; want the newest English one", got, ok)
	}
	if got, ok := Latest("v", func(l string) bool { return l == "fre" }); !ok || got.URL != "f" {
		t.Errorf("Latest(fre) = %+v, %v", got, ok)
	}
	if _, ok := Latest("v", func(l string) bool { return l == "spa" }); ok {
		t.Error("Latest found a language nobody synced")
	}
	if _, ok := Latest("nope", nil); ok {
		t.Error("Latest found a sync for another video")
	}

	// Syncing the same sub again replaces its entry rather than stacking.
	Remember("v", Synced{URL: "e1", Lang: "eng", Path: eng1})
	if got, _ := Latest("v", isEng); got.URL != "e1" {
		t.Errorf("Latest(eng) = %q after re-syncing e1, want e1", got.URL)
	}
	os.Remove(eng1)
	if got, ok := Latest("v", isEng); !ok || got.URL != "e2" {
		t.Errorf("Latest(eng) = %+v, %v after e1's file went; want e2", got, ok)
	}
}
