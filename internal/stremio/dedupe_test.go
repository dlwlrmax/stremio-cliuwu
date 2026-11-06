package stremio

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two callers asking for the same episode at once — the picker and the
// autopick both do — must cost the addon one request, not two.
func TestGetStreamsSharesAnInFlightFetch(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0") // GetStreams skips 127.* and localhost
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, `{"streams":[{"url":"http://x/a"}]}`)
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	addons := []Addon{{
		TransportURL: srv.URL + "/manifest.json",
		Manifest:     Manifest{ID: "x", Name: "x", Types: []string{"series"}, Resources: []any{"stream"}},
	}}
	const id = "ttDEDUPE:1:1"
	InvalidateStreams(id)

	var wg sync.WaitGroup
	got := make([]int, 2)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = len(GetStreams(addons, "series", id))
		}()
	}
	wg.Wait()

	if n := hits.Load(); n != 1 {
		t.Errorf("addon was asked %d times, want 1", n)
	}
	if got[0] != 1 || got[1] != 1 {
		t.Errorf("results = %v, want both callers to get the stream", got)
	}
}
