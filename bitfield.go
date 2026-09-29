package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Stremio stores per-episode watched state on a library item as one compact
// bitfield string:
//
//	<anchorVideoId>:<length>:<base64(zlib(bits))>
//
// The anchor video id is itself "<showID>:<season>:<episode>" and contains
// colons, so only the last two colon-separated fields are the length and the
// payload — everything before them is the anchor id. No watched state is the
// literal "undefined:1:<zlib of nothing>".
//
// Bit i is buf[i/8] & (1 << (i%8)) — least-significant bit first. Index i is
// the i-th video in the show's flat meta list ordered by season, then episode.
// length is the last watched index plus one, and the anchor id is the video at
// that index: the most recently watched episode.
//
// Go's zlib encoder differs byte-for-byte from the one Stremio uses (pako),
// but both emit a standard zlib stream, so either side can read the other.
//
// Test vectors (see the manual check in the report):
//
//	Encode over tt2934286 S1E1..E5 -> tt2934286:1:5:...  (bits 0..4 set)
//	Decode "tt2934286:1:5:eJyTZwAAAEAAIA==" -> anchor tt2934286:1:5, bits 0..4
//	Empty watched state            -> "undefined:1:eJwDAAAAAAE="

// EncodeWatchedBitfield renders the watched members of videos as Stremio's
// bitfield string. videos must already be in episode order (season, then
// episode); watched is keyed by video id.
func EncodeWatchedBitfield(videos []string, watched map[string]bool) string {
	last := -1
	for i, v := range videos {
		if watched[v] {
			last = i
		}
	}

	// Nothing watched: no anchor, one bit of zeroes. This is the literal
	// Stremio writes, and Go's zlib matches it byte-for-byte for an empty
	// input.
	if last < 0 {
		return "undefined:1:" + base64.StdEncoding.EncodeToString(zlibDeflate(nil))
	}

	length := last + 1
	buf := make([]byte, (length+7)/8)
	for i := 0; i < length; i++ {
		if watched[videos[i]] {
			buf[i/8] |= 1 << (uint(i) % 8)
		}
	}

	return videos[last] + ":" + strconv.Itoa(length) + ":" +
		base64.StdEncoding.EncodeToString(zlibDeflate(buf))
}

// DecodeWatchedBitfield parses the bitfield string, returning the anchor video
// id, the encoded bit length, and the set bit indices (map[i] may be used
// directly as a set). Bits past the decompressed buffer are ignored.
func DecodeWatchedBitfield(s string) (anchorID string, length int, bits map[int]bool, err error) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 {
		return "", 0, nil, errors.New("bitfield: missing payload")
	}
	payload := s[i+1:]

	j := strings.LastIndexByte(s[:i], ':')
	if j < 0 {
		return "", 0, nil, errors.New("bitfield: missing length")
	}
	length, err = strconv.Atoi(s[j+1 : i])
	if err != nil || length < 0 {
		return "", 0, nil, errors.New("bitfield: bad length")
	}
	anchorID = s[:j]

	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return anchorID, length, nil, err
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return anchorID, length, nil, err
	}
	buf, err := io.ReadAll(zr)
	_ = zr.Close()
	if err != nil {
		return anchorID, length, nil, err
	}

	bits = map[int]bool{}
	for i := 0; i < length; i++ {
		if i/8 >= len(buf) {
			break
		}
		if buf[i/8]&(1<<(uint(i)%8)) != 0 {
			bits[i] = true
		}
	}
	return anchorID, length, bits, nil
}

func zlibDeflate(b []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

// ── Index space ───────────────────────────────────────────────────────────────

// orderedEpisodes is the episode order a bitfield index maps onto: the show's
// videos sorted by season, then episode. The cached meta supplies the full
// list when the title has been browsed; otherwise only the episodes the store
// has already recorded can be addressed, because an episode never touched
// locally has no index to send or receive. Callers hold hist.mu.
func orderedEpisodes(showID string, sh *showState) [][2]int {
	if eps := epsFromMeta(showID); len(eps) > 0 {
		return eps
	}
	return epsFromStore(sh)
}

func epsFromMeta(showID string) [][2]int {
	sm, ok := cacheSeries.Get(showID)
	if !ok {
		return nil
	}
	out := make([][2]int, 0, len(sm.Videos))
	for _, v := range sm.Videos {
		if v.Episode <= 0 {
			continue
		}
		out = append(out, [2]int{v.Season, v.Episode})
	}
	sortSeEp(out)
	return out
}

func epsFromStore(sh *showState) [][2]int {
	if sh == nil {
		return nil
	}
	out := make([][2]int, 0, len(sh.Eps))
	for k := range sh.Eps {
		s, e := parseEpKey(k)
		if e <= 0 {
			continue
		}
		out = append(out, [2]int{s, e})
	}
	sortSeEp(out)
	return out
}

func sortSeEp(eps [][2]int) {
	sort.Slice(eps, func(i, j int) bool {
		if eps[i][0] != eps[j][0] {
			return eps[i][0] < eps[j][0]
		}
		return eps[i][1] < eps[j][1]
	})
}
