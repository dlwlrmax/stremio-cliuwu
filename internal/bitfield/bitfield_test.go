package bitfield

import (
	"slices"
	"testing"
)

// Vector from stremio-core's own docs for WatchedField.
func TestDecodeStremioCoreVector(t *testing.T) {
	anchor, length, bits, err := Decode("tt2934286:1:5:5:eJyTZwAAAEAAIA==")
	if err != nil {
		t.Fatal(err)
	}
	if anchor != "tt2934286:1:5" || length != 5 {
		t.Errorf("anchor %q length %d", anchor, length)
	}
	for i := 0; i < 5; i++ {
		if !bits[i] {
			t.Errorf("bit %d should be set", i)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	vids := []string{"a", "b", "c", "d", "e"}
	enc := Encode(vids, map[string]bool{"a": true, "c": true, "d": true})
	anchor, length, bits, err := Decode(enc)
	if err != nil || anchor != "d" || length != 4 {
		t.Fatalf("%q %d %v", anchor, length, err)
	}
	if got := Aligned(length, 3, 5, bits); !slices.Equal(got, []int{0, 2, 3}) {
		t.Errorf("got %v", got)
	}
}

// The anchor is stored at bit length-1; where it sits in the local list shifts
// every other bit with it.
func TestAlignedWatchedShiftsByAnchor(t *testing.T) {
	// Remote list is 100 long: episodes 1-3 and 48-50 watched, anchor is 50
	// (index 49, length 50).
	bits := map[int]bool{0: true, 1: true, 2: true, 47: true, 48: true, 49: true}

	// Local list knows only episodes 48..50, anchor at local index 2.
	// offset = 50-2-1 = 47, so local i reads stored bit i+47: all three set.
	if got := Aligned(50, 2, 3, bits); !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("sparse tail: got %v", got)
	}

	// Same bitfield, local list of episodes 1..50 (anchor at index 49):
	// offset 0, so absolute indexing and the anchor-relative one agree.
	if got := Aligned(50, 49, 50, bits); !slices.Equal(got, []int{0, 1, 2, 47, 48, 49}) {
		t.Errorf("full list: got %v", got)
	}

	if got := Aligned(50, -1, 3, bits); got != nil {
		t.Errorf("missing anchor must mark nothing, got %v", got)
	}
}
