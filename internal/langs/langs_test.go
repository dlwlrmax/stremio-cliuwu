package langs

import "testing"

func TestPreferredDedupesAliases(t *testing.T) {
	got := Preferred("eng, en, English, spa")
	if len(got) != 2 || got[0] != "English" || got[1] != "Spanish" {
		t.Errorf("got %v", got)
	}
}

func TestNameNormalises(t *testing.T) {
	cases := map[string]string{
		"eng":         "English",
		" EN ":        "English",
		"english":     "English",
		"en-GB":       "English", // region only narrows a known language
		"pt_BR":       "Portuguese",
		"fre":         "French",
		"":            "unknown",
		"klingon-ish": "klingon-ish", // free text passes through
	}
	for in, want := range cases {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRankPutsUnlistedLast(t *testing.T) {
	prefs := []string{"Vietnamese", "English"}
	if Rank(prefs, "Vietnamese") != 0 || Rank(prefs, "English") != 1 {
		t.Error("listed languages should rank by position")
	}
	if Rank(prefs, "French") <= Rank(prefs, "English") {
		t.Error("an unlisted language must rank after every listed one")
	}
}

func TestReferenceListsEveryLanguageOnce(t *testing.T) {
	ref := Reference()
	if len(ref) == 0 {
		t.Fatal("empty reference")
	}
	seen := map[string]bool{}
	for _, line := range ref {
		if seen[line] {
			t.Errorf("duplicate line %q", line)
		}
		seen[line] = true
	}
}
