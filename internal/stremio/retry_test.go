package stremio

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A self-hosted aggregator (AIOStreams) can take several seconds to build its
// first manifest after idling. The launch fetch gives up early to keep startup
// quick, so the addon came back failed and stayed failed for the session:
// its streams never appeared until the addons screen fetched it again.
func TestRetryFailedRecoversSlowManifest(t *testing.T) {
	oldFirst, oldRetry := manifestTimeout, manifestRetryTimeout
	manifestTimeout, manifestRetryTimeout = 50*time.Millisecond, 2*time.Second
	t.Cleanup(func() { manifestTimeout, manifestRetryTimeout = oldFirst, oldRetry })

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"id":"slow","name":"Slow","resources":["stream"],"types":["movie"]}`))
	}))
	defer slow.Close()
	var fastHits atomic.Int32
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fastHits.Add(1)
		w.Write([]byte(`{"id":"fast","name":"Fast"}`))
	}))
	defer fast.Close()

	al := AddonList{Items: []AddonRef{{URL: fast.URL + "/manifest.json"}, {URL: slow.URL + "/manifest.json"}}}
	first := LoadAddons(al)
	if first[1].Err == nil {
		t.Fatal("precondition: the slow manifest should miss the launch deadline")
	}

	got, recovered := RetryFailed(first)
	if !recovered {
		t.Fatal("want recovered=true")
	}
	if got[1].Err != nil || got[1].Manifest.Name != "Slow" {
		t.Fatalf("slow addon not recovered: %+v", got[1])
	}
	if got[0].Manifest.Name != "Fast" {
		t.Errorf("order or healthy addon changed: %+v", got[0])
	}
	if n := fastHits.Load(); n != 1 {
		t.Errorf("healthy addon refetched: %d hits", n)
	}
	if first[1].Err == nil {
		t.Error("input slice was mutated; it's shared with readers")
	}
}

func TestRetryFailedNothingToDo(t *testing.T) {
	in := []Addon{{TransportURL: "https://x.test/manifest.json"}}
	if _, recovered := RetryFailed(in); recovered {
		t.Error("nothing failed, nothing should be reported recovered")
	}
}
