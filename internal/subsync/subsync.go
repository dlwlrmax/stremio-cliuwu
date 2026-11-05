package subsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNoAlass means neither alass-cli nor alass is on PATH.
	ErrNoAlass = errors.New("alass-cli not found")
	// ErrTooLarge means a subtitle body was over the 4 MB cap. No real
	// subtitle is anywhere near it; what is, is the wrong kind of file.
	ErrTooLarge = errors.New("subtitle larger than 4 MB")
)

const maxBody = 4 << 20

// Seams for tests.
var (
	lookPath   = exec.LookPath
	httpClient = &http.Client{Timeout: 30 * time.Second}
)

// The temp directory holds everything this package produces, and is the only
// place the player will load a local subtitle from. It is made on first use
// and removed at exit.
var tmp struct {
	sync.Mutex
	dir string
}

func tempDir() (string, error) {
	tmp.Lock()
	defer tmp.Unlock()
	if tmp.dir != "" {
		if _, err := os.Stat(tmp.dir); err == nil {
			return tmp.dir, nil
		}
	}
	dir, err := os.MkdirTemp("", "stremio-cliuwu-subs-")
	if err != nil {
		return "", err
	}
	tmp.dir = dir
	return dir, nil
}

// Cleanup removes the temp directory. Safe to call repeatedly, and when
// nothing was ever created.
func Cleanup() {
	tmp.Lock()
	dir := tmp.dir
	tmp.dir = ""
	tmp.Unlock()
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// Owns reports whether path is a file inside the temp directory. Symlinks are
// resolved first, so a link planted inside it cannot point the player at
// something else, and the directory itself does not count.
func Owns(path string) bool {
	tmp.Lock()
	dir := tmp.dir
	tmp.Unlock()
	if dir == "" || !filepath.IsAbs(path) {
		return false
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	p := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		// Not there yet: resolve what is, so a missing file under a
		// symlinked directory is still judged by where that directory is.
		parent, perr := filepath.EvalSymlinks(filepath.Dir(p))
		if perr != nil {
			return false
		}
		resolved = filepath.Join(parent, filepath.Base(p))
	}
	return strings.HasPrefix(resolved, root+string(filepath.Separator))
}

// Sync downloads ref and sub, normalises both to SubRip, aligns sub to ref
// with alass, and returns the path of the retimed file inside the temp
// directory.
func Sync(ctx context.Context, refURL, subURL string) (string, error) {
	bin, err := findAlass()
	if err != nil {
		return "", err
	}
	ref, err := fetch(ctx, refURL)
	if err != nil {
		return "", fmt.Errorf("reference subtitle: %w", err)
	}
	sub, err := fetch(ctx, subURL)
	if err != nil {
		return "", fmt.Errorf("subtitle: %w", err)
	}

	// Checked before anything is written or run: alass's own complaint about
	// a web page or an empty file ("invalid digit found in string") does not
	// say which file was wrong, or that it was never a subtitle at all.
	refSRT, err := normalise(ref)
	if err != nil {
		return "", fmt.Errorf("reference subtitle: %w", err)
	}
	subSRT, err := normalise(sub)
	if err != nil {
		return "", fmt.Errorf("subtitle: %w", err)
	}

	root, err := tempDir()
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(root, "s-")
	if err != nil {
		return "", err
	}
	refPath := filepath.Join(work, "ref.srt")
	subPath := filepath.Join(work, "sub.srt")
	outPath := filepath.Join(work, "out.srt")
	if err := os.WriteFile(refPath, refSRT, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(subPath, subSRT, 0o600); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin, refPath, subPath, outPath)
	// A killed shell wrapper can leave a child holding the output pipe open.
	cmd.WaitDelay = 2 * time.Second
	// alass-cli reports errors on stdout, so the two are read together.
	out, runErr := cmd.CombinedOutput()
	os.Remove(refPath)
	os.Remove(subPath)
	if runErr != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("sync cancelled: %w", ctx.Err())
		}
		if msg := lastLines(string(out), 3); msg != "" {
			return "", fmt.Errorf("alass: %s", msg)
		}
		return "", fmt.Errorf("alass: %w", runErr)
	}
	if _, err := os.Stat(outPath); err != nil {
		return "", errors.New("alass produced no output")
	}
	return outPath, nil
}

func findAlass() (string, error) {
	for _, name := range []string{"alass-cli", "alass"} {
		if p, err := lookPath(name); err == nil {
			return p, nil
		}
	}
	return "", ErrNoAlass
}

// fetch reads a URL into memory, refusing anything over the cap.
func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, ErrTooLarge
	}
	return body, nil
}

// lastLines is the tail of a tool's output, without the "run with
// RUST_BACKTRACE" hint, joined into one line. alass prints a chain of "caused
// by:" lines with the innermost last, and the innermost alone is usually the
// least useful: the line above it says what was expected and what was found.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" && !strings.Contains(l, "RUST_BACKTRACE") {
			keep = append([]string{l}, keep...)
		}
	}
	return strings.Join(keep, " — ")
}

// normalise turns a downloaded body into SubRip, or says why it can't be a
// subtitle. Hosts answer 200 with a rate-limit page or nothing at all often
// enough that this is worth saying in plain words.
func normalise(raw []byte) ([]byte, error) {
	body := bytes.TrimSpace(bytes.TrimPrefix(raw, utf8BOM))
	switch {
	case len(body) == 0:
		return nil, errors.New("empty file")
	case body[0] == '<':
		return nil, errors.New("looks like a web page, not a subtitle")
	}
	out := ToSRT(raw)
	if bytes.HasPrefix(body, []byte("WEBVTT")) && len(bytes.TrimSpace(out)) == 0 {
		return nil, errors.New("WebVTT file has no cues")
	}
	return out, nil
}
