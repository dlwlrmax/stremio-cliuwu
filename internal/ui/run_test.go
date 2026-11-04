package ui

import (
	"os"
	"testing"
)

// quiet swaps stdout and stderr for the null device: parseArgs prints for
// --version, --help and bad options, and the test only wants its result.
func quiet(t *testing.T) {
	t.Helper()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = null, null
	t.Cleanup(func() { os.Stdout, os.Stderr = oldOut, oldErr; null.Close() })
}

func TestParseArgsSearchFlagsAndMultiWordQuery(t *testing.T) {
	o, run := parseArgs([]string{"-sf", "-a", "chuunibyou", "demo", "ni", "koi"})
	if !run {
		t.Fatal("a normal invocation should run the app")
	}
	if !o.search || !o.first || o.kind != "anime" || o.query != "chuunibyou demo ni koi" {
		t.Errorf("got %+v", o)
	}
}

func TestParseArgsKinds(t *testing.T) {
	cases := map[string]string{"-m": "movie", "--movies": "movie", "-t": "show", "--shows": "show", "-a": "anime", "--anime": "anime"}
	for flag, want := range cases {
		o, run := parseArgs([]string{flag})
		if !run || o.kind != want {
			t.Errorf("%s: got kind %q run=%v, want %q", flag, o.kind, run, want)
		}
	}
}

func TestParseArgsPlainSearchFlagWithoutQuery(t *testing.T) {
	o, run := parseArgs([]string{"-s"})
	if !run || !o.search || o.first || o.query != "" {
		t.Errorf("got %+v run=%v", o, run)
	}
}

func TestParseArgsInfoFlagsExitWithoutRunning(t *testing.T) {
	quiet(t)
	for _, args := range [][]string{{"--version"}, {"-v"}, {"--config"}, {"-h"}, {"--help"}, {"--bogus"}} {
		if _, run := parseArgs(args); run {
			t.Errorf("%v should print and stop, not start the app", args)
		}
	}
}

func TestParseArgsNoArgsOpensTheMenu(t *testing.T) {
	o, run := parseArgs(nil)
	if !run || o != (launchOpts{}) {
		t.Errorf("got %+v run=%v", o, run)
	}
	if startupCmd(o) != nil {
		t.Error("no options means no startup screen: the menu stays")
	}
}
