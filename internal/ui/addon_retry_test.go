package ui

import (
	"errors"
	"testing"

	"stremio-cliuwu/internal/stremio"
)

func failedAddons() []stremio.Addon {
	return []stremio.Addon{
		{TransportURL: "https://ok.test/manifest.json", Manifest: stremio.Manifest{Name: "OK"}},
		{TransportURL: "https://slow.test/manifest.json", Err: errors.New("deadline exceeded")},
	}
}

func recoveredAddons() []stremio.Addon {
	a := failedAddons()
	a[1] = stremio.Addon{TransportURL: a[1].TransportURL, Manifest: stremio.Manifest{Name: "AIOStreams"}}
	return a
}

// A manifest that missed the launch deadline gets a second, patient attempt;
// without it the addon stayed failed until the addons screen refetched it.
func TestRetryFailedCmdOnlyWhenSomethingFailed(t *testing.T) {
	old := ctx
	t.Cleanup(func() { ctx = old })

	ctx = &appCtx{}
	ctx.setAddons(recoveredAddons())
	if retryFailedCmd() != nil {
		t.Error("nothing failed, so nothing should be retried")
	}
	ctx.setAddons(failedAddons())
	if retryFailedCmd() == nil {
		t.Error("a failed manifest should be retried")
	}
}

func TestAddonsRecoveredReplacesAddons(t *testing.T) {
	old := ctx
	t.Cleanup(func() { ctx = old })
	ctx = &appCtx{}
	ctx.setAddons(failedAddons())

	a := newApp(&subsScreen{})
	a.Update(addonsRecoveredMsg{gen: ctx.gen, addons: recoveredAddons()})
	if got := ctx.Addons()[1]; got.Err != nil || got.Manifest.Name != "AIOStreams" {
		t.Fatalf("recovered addon not installed: %+v", got)
	}
}

// The addons screen can replace the list while the retry is out; its answer
// is newer and must not be overwritten by the stale one.
func TestAddonsRecoveredDroppedWhenListChanged(t *testing.T) {
	old := ctx
	t.Cleanup(func() { ctx = old })
	ctx = &appCtx{}
	ctx.setAddons(failedAddons())
	stale := ctx.gen
	ctx.setAddons(failedAddons()[:1]) // the user removed the slow addon meanwhile

	a := newApp(&subsScreen{})
	a.Update(addonsRecoveredMsg{gen: stale, addons: recoveredAddons()})
	if n := len(ctx.Addons()); n != 1 {
		t.Fatalf("stale retry overwrote a newer list: %d addons", n)
	}
}

// A stream list opened while the addon was down is missing its results, so it
// fetches again once the addon is back.
func TestStreamScreenRefetchesWhenAddonRecovers(t *testing.T) {
	old := ctx
	t.Cleanup(func() { ctx = old })
	ctx = &appCtx{}

	s := newStreamScreen(streamTarget{VideoID: "tt1", MediaType: "movie"})
	s.loaded = true
	before := s.id
	if _, cmd := s.Update(addonsRecoveredMsg{}); cmd == nil || s.id == before {
		t.Error("stream screen should start a fresh fetch")
	}
}
