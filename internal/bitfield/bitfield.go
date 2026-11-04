// Package bitfield reads and writes Stremio's per-episode watched bitfield.
package bitfield

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"io"
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

// Encode renders the watched members of videos as Stremio's
// bitfield string. videos must already be in episode order (season, then
// episode); watched is keyed by video id.
func Encode(videos []string, watched map[string]bool) string {
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

// Decode parses the bitfield string, returning the anchor video
// id, the encoded bit length, and the set bit indices (map[i] may be used
// directly as a set). Bits past the decompressed buffer are ignored.
func Decode(s string) (anchorID string, length int, bits map[int]bool, err error) {
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

// Aligned returns the indices of the n videos that a decoded bitfield
// marks watched, aligned the way stremio-core does it.
//
// The stored bits are not addressed by absolute position. The anchor sits at
// stored bit anchorLen-1 and at anchorIdx in the current list, and every other
// bit is placed relative to it: video i takes stored bit i+offset, with
// offset = anchorLen - anchorIdx - 1. That is what lets a list that gained or
// lost videos at the front still line up. Reading bit i as video i is only
// right when offset is 0, i.e. when the local list matches Stremio's from the
// start up to the anchor.
func Aligned(anchorLen, anchorIdx, n int, bits map[int]bool) []int {
	if anchorIdx < 0 || anchorIdx >= n {
		return nil
	}
	offset := anchorLen - anchorIdx - 1
	var out []int
	for i := 0; i < n; i++ {
		if j := i + offset; j >= 0 && j < anchorLen && bits[j] {
			out = append(out, i)
		}
	}
	return out
}

func zlibDeflate(b []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}
