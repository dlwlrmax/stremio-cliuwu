package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The releases API needs no auth for a public repo and returns the tag of the
// newest release, which is all we need — the tag is the version.
const releasesURL = "https://api.github.com/repos/ilovealienz/stremio-cliuwu/releases/latest"

type updateState int

const (
	updateUnknown updateState = iota
	updateCurrent
	updateAvailable
	updateAhead // running a build newer than the last release
)

type updateInfo struct {
	State  updateState
	Latest string // tag of the newest release
	URL    string // its page, for opening
}

type updateCheckedMsg struct{ Info updateInfo }

var (
	updateMu     sync.Mutex
	updateCache  updateInfo
	updateAt     time.Time
	updateTTL    = 6 * time.Hour
	updateLoaded bool
)

// CheckUpdate asks GitHub for the latest release, at most once every few
// hours.
//
// Cached because this fires whenever settings is opened: without it, flicking
// in and out of the screen would hit the API repeatedly for an answer that
// changes at most once per release.
func CheckUpdate() tea.Cmd {
	return func() tea.Msg {
		updateMu.Lock()
		if updateLoaded && time.Since(updateAt) < updateTTL {
			info := updateCache
			updateMu.Unlock()
			return updateCheckedMsg{Info: info}
		}
		updateMu.Unlock()

		var resp struct {
			TagName string `json:"tag_name"`
			HTMLURL string `json:"html_url"`
			Draft   bool   `json:"draft"`
			Pre     bool   `json:"prerelease"`
		}

		info := updateInfo{}
		if err := getJSONTimeout(releasesURL, &resp, 8*time.Second); err == nil &&
			resp.TagName != "" && !resp.Draft {
			info = updateInfo{
				State:  compareVersions(version, resp.TagName),
				Latest: resp.TagName,
				URL:    resp.HTMLURL,
			}
		}

		updateMu.Lock()
		updateCache, updateAt, updateLoaded = info, time.Now(), true
		updateMu.Unlock()

		return updateCheckedMsg{Info: info}
	}
}

// compareVersions works out where the running build sits relative to a
// release tag.
//
// The build version comes from git describe, so a build made after a release
// reads "0.6.0-3-gabc1234" — the same base version with commits on top. That
// is ahead of v0.6.0, not behind it, and comparing the strings would have
// suggested updating to a release already included in what's running.
func compareVersions(running, tag string) updateState {
	if running == "" || running == "dev" {
		return updateUnknown
	}

	base, exact := splitDescribe(running)
	latest := strings.TrimPrefix(strings.TrimSpace(tag), "v")

	switch cmp := compareParts(base, latest); {
	case cmp < 0:
		return updateAvailable
	case cmp > 0:
		return updateAhead
	case !exact:
		return updateAhead // the tag plus commits, or a modified tree
	}
	return updateCurrent
}

// splitDescribe pulls the tag out of a git describe string and reports
// whether the build is exactly that tag.
//
// describe appends what makes a build differ from its tag: "-3-gabc1234" for
// commits since, "-dirty" for uncommitted changes, or both. Any of them means
// this isn't the release, so the presence of a suffix is what matters rather
// than parsing a commit count out of it — reading "dirty" as zero commits
// made a modified tree indistinguishable from the tag itself.
func splitDescribe(v string) (base string, exact bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")

	if i := strings.Index(v, "-"); i >= 0 {
		return v[:i], false
	}
	return v, true
}

// compareParts compares dotted version numbers a segment at a time, so 0.10.0
// sorts above 0.9.0 rather than below it as a string comparison would.
func compareParts(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")

	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := 0, 0
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Line is the one-line summary shown at the foot of settings.
func (u updateInfo) Line() string {
	switch u.State {
	case updateCurrent:
		return good("● up to date")
	case updateAvailable:
		return bad("● "+u.Latest+" available") + stHint.Render("  ·  u to open")
	case updateAhead:
		return stWarn.Render("● unreleased build") + stHint.Render("  ·  latest is "+u.Latest)
	}
	return ""
}
