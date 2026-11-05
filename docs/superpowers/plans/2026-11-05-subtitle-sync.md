# Subtitle sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sync a mistimed subtitle to a well-timed one without reading the video: in the app's subtitle list (`m` / `S`), in mpv with one key (`Alt+n`), plus a 10-minute audio fallback in mpv.

**Architecture:** A new `internal/subsync` package fetches two subtitles, normalises them to SRT (WebVTT converted in plain Go), and aligns them with `alass-cli`. `Player.AddLocalSubtitle` loads the result into mpv from the package's temp directory only. The subtitle screen gets two keys. The mpv pieces are Lua changes in the dotfiles `autosubsync` script.

**Tech Stack:** Go 1.22 (standard library only, no new modules), Bubble Tea, `alass-cli` (external), mpv Lua scripting, ffmpeg (mpv script only).

**Spec:** `docs/superpowers/specs/2026-11-05-subtitle-sync-design.md`

## Global Constraints

- Go module `stremio-cliuwu`, `go 1.22` in `go.mod`: no new dependencies; tests use `testing` and `net/http/httptest` only.
- `alass` is the engine; `alass-cli` must be on `PATH`, looked up as `alass-cli` then `alass`. The app does not install it.
- Reference subtitle = a subtitle from the addon list. Never an embedded track.
- Limits are fixed constants, not settings: 4 MB cap per subtitle body, 30 s fetch timeout, 600 s audio clip length.
- `alass-cli` reports errors on **stdout**. Capture stdout and stderr together; surface the last non-empty line without the `RUST_BACKTRACE` hint.
- `AddLocalSubtitle` accepts only paths inside the subsync temp directory (`filepath.Clean` + prefix check). `AddSubtitle` stays http(s)-only and unchanged.
- Keys: `m` marks the reference, `S` syncs the highlighted sub; mpv quick-sync is `Alt+n` (`n` belongs to `autosub.lua`, `N` opens the existing menu).
- Temp directory: `os.MkdirTemp("", "stremio-cliuwu-subs-")`, created on first use, removed at app exit.
- Work happens in two repos: the app (`/home/kienct/app/stremio-cliuwu`) and the dotfiles (`/home/kienct/.dotfiles`, stow layout: edit `mpv/.config/mpv/...`, which `~/.config/mpv` symlinks to).

## Review Focus

1. WebVTT with a BOM, `NOTE`/`STYLE` blocks and cue settings (`align:start position:0%`): must produce a clean SRT (Task 1).
2. A subtitle host that returns an HTML error page or a >4 MB body with a 200 status: sync must fail with a readable error, not hand garbage to `alass` (Task 2).
3. `alass-cli` exits non-zero and prints its reason on stdout: the user must see that reason (Task 2).
4. Pressing `S` twice quickly, or leaving the subtitle screen mid-sync: no second `alass` run piling up, and no result loaded after the screen is gone (Task 4).
5. A synced result loaded from a temp path must still mark the originating list row as "on", and a path outside the temp directory must never reach mpv (Tasks 3 and 4).

---

### Task 1: WebVTT-to-SRT normaliser

**Files:**
- Create: `internal/subsync/srt.go`
- Test: `internal/subsync/srt_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func ToSRT(data []byte) []byte` in package `subsync`. Data that does not start with `WEBVTT` (after an optional UTF-8 BOM) is returned unchanged, BOM included. WebVTT input returns SubRip text with numbered cues, `HH:MM:SS,mmm` timestamps and a blank line between cues.

- [ ] **Step 1: Write the failing tests** in `srt_test.go` (table-driven, `TestToSRT`, one subtest per case):
  - plain SubRip input (`1\n00:00:01,000 --> 00:00:02,000\nhi\n`) returns the identical bytes;
  - empty input returns empty;
  - `\xef\xbb\xbfWEBVTT\n\n00:01.000 --> 00:02.500\nhello\n` returns `1\n00:00:01,000 --> 00:00:02,500\nhello\n\n` (BOM stripped, missing hour added);
  - cue identifiers (`intro\n00:00:01.000 --> ...`) are dropped and cues renumbered from 1;
  - cue settings (`00:00:01.000 --> 00:00:02.000 align:start position:0%`) are dropped from the timing line;
  - `NOTE ...` and `STYLE ...` blocks (and the `WEBVTT - title` header line) are skipped, including multi-line ones;
  - inline tags `<c.yellow>x</c>`, `<v Bob>hi`, `<i>y</i>` are stripped from text lines (`<i>` and `<b>` are kept: SRT supports them);
  - `\r\n` line endings behave like `\n`;
  - a multi-line cue keeps its line breaks.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/subsync/ -run TestToSRT -v`
Expected: FAIL, `undefined: ToSRT` (build failure).

- [ ] **Step 3: Implement `ToSRT(data []byte) []byte` in `internal/subsync/srt.go`.** Split on blank lines into blocks; a block is a cue if one of its first two lines contains `-->`; take only `start --> end` from that line (first two whitespace-separated timestamps), normalise each timestamp to `HH:MM:SS,mmm`; keep the following lines as text with tags other than `<i>`, `<b>`, `<u>` removed (a small hand-written scan, no regexp needed beyond a tag pattern).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/subsync/ -v`
Expected: PASS, all subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/subsync/srt.go internal/subsync/srt_test.go
git commit -m "feat(subsync): convert WebVTT to SubRip"
```

---

### Task 2: Fetch, align and temp directory

**Files:**
- Create: `internal/subsync/subsync.go`
- Test: `internal/subsync/subsync_test.go`

**Interfaces:**
- Consumes: `ToSRT` from Task 1.
- Produces, all in package `subsync`:
  - `var ErrNoAlass = errors.New("alass-cli not found")`
  - `var ErrTooLarge = errors.New("subtitle larger than 4 MB")`
  - `func Sync(ctx context.Context, refURL, subURL string) (outPath string, err error)`
  - `func Owns(path string) bool` — true only if `path`, after `filepath.Clean` and symlink resolution of the directory part, is inside the temp directory. False when the directory was never created.
  - `func Cleanup()` — removes the temp directory; safe to call repeatedly and when nothing was created.
  - package-private seam for tests: `var lookPath = exec.LookPath` and `var httpClient = &http.Client{Timeout: 30 * time.Second}`.

- [ ] **Step 1: Write the failing tests** in `subsync_test.go`. Helper `fakeAlass(t, script string)` writes an executable shell script named `alass-cli` into `t.TempDir()` and sets `lookPath` to a func that resolves only that file (restored with `t.Cleanup`). Tests:
  - `TestSyncSuccess`: `httptest` server serves `/ref.srt` and `/sub.vtt` (WebVTT body under a `.vtt` name); fake alass is `cp "$2" "$3"`; `Sync` returns a path where `Owns(path)` is true and the file contains `-->` with a comma timestamp (proves the VTT was converted before alass saw it).
  - `TestSyncNoAlass`: `lookPath` returns an error for both names; `errors.Is(err, ErrNoAlass)`.
  - `TestSyncTooLarge`: server returns 4 MB + 1 byte; `errors.Is(err, ErrTooLarge)` and alass is never run (fake script touches a marker file; marker absent).
  - `TestSyncAlassErrorOnStdout`: fake prints `error: parsing failed\ncaused by: bad digit\n\nnot: run with environment variable 'RUST_BACKTRACE=1'` to stdout and exits 1; error text contains `caused by: bad digit` and not `RUST_BACKTRACE`.
  - `TestSyncHTTPStatus`: server returns 404; error mentions `404`.
  - `TestSyncCancelled`: fake alass is `sleep 30`; cancel the context after 100 ms; `Sync` returns within 2 s with `context.Canceled` in the chain.
  - `TestOwns`: false before any sync; true for the returned path; false for `outPath + "/../x"` escape, for `/etc/passwd`, and for a symlink inside the temp dir pointing outside.
  - `TestCleanup`: after `Cleanup()` the directory is gone and a second `Cleanup()` does not panic.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/subsync/ -v`
Expected: FAIL, undefined `Sync`, `Owns`, `Cleanup`.

- [ ] **Step 3: Implement the package.**
  - `Sync`: fetch both bodies with `httpClient` bound to `ctx` (`http.NewRequestWithContext`), non-2xx status → error containing the status code, body read through `io.LimitReader(resp.Body, 4<<20+1)` and `ErrTooLarge` when it exceeds `4<<20`. Run `ToSRT` on each, write `ref.srt` and `sub.srt` into a fresh subdirectory of the temp dir (`os.MkdirTemp(tempDir(), "s-")`), run `alass-cli ref.srt sub.srt out.srt` with `exec.CommandContext` and combined output. On failure return an error whose text is the last non-empty output line that does not contain `RUST_BACKTRACE` (fall back to the exit error). Look the binary up with `lookPath("alass-cli")` then `lookPath("alass")`; both failing returns `ErrNoAlass`.
  - Temp dir: package-level `sync.Mutex`-guarded path, created lazily by `tempDir()`.
  - Delete the downloaded `ref.srt` and `sub.srt` after alass finishes; keep `out.srt`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/subsync/ -race -v`
Expected: PASS, all tests, no race reports.

- [ ] **Step 5: Commit**

```bash
git add internal/subsync/subsync.go internal/subsync/subsync_test.go
git commit -m "feat(subsync): fetch, normalise and align two subtitles with alass"
```

---

### Task 3: Load a local synced file into mpv

**Files:**
- Modify: `internal/player/player.go` (add the method next to `AddSubtitle`, around line 211)
- Modify: `internal/ui/run.go` (the `cleanup` closure, around line 178)
- Test: `internal/player/player_unit_test.go`

**Interfaces:**
- Consumes: `subsync.Owns(path string) bool`, `subsync.Cleanup()` from Task 2.
- Produces: `func (p *Player) AddLocalSubtitle(path, title, lang string) tea.Cmd`. Returns a `SubtitleAddedMsg` whose `URL` is `path`. A path for which `subsync.Owns` is false returns `SubtitleAddedMsg{URL: path, Title: title, Err: errors.New("subtitle file is not one the app generated")}` without contacting mpv.

- [ ] **Step 1: Write the failing test** `TestAddLocalSubtitleRejectsForeignPath` in `player_unit_test.go`: build a `Player` the way the other unit tests in that file do (a zero-value `&Player{}` is enough because the rejection happens before any IPC), call `AddLocalSubtitle("/etc/passwd", "x", "eng")()` and assert the message is a `SubtitleAddedMsg` with a non-nil `Err` and `URL == "/etc/passwd"`. Also assert a relative path (`"../x.srt"`) is rejected.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/player/ -run TestAddLocalSubtitle -v`
Expected: FAIL, `p.AddLocalSubtitle undefined`.

- [ ] **Step 3: Implement `AddLocalSubtitle`** in `player.go`: same shape as `AddSubtitle` (a `tea.Cmd` closure; title defaults to `"subtitle"`; `commandWait(30*time.Second, "sub-add", path, "select", title, lang)`), with the `subsync.Owns` check first. Import `stremio-cliuwu/internal/subsync`. In `run.go`'s `cleanup` closure add `subsync.Cleanup()` after `store.FlushHistory()`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/player/ ./internal/subsync/ -race && go build ./...`
Expected: PASS, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/player/player.go internal/player/player_unit_test.go internal/ui/run.go
git commit -m "feat(player): load app-generated subtitle files into mpv"
```

---

### Task 4: Subtitle list keys `m` and `S`

**Files:**
- Modify: `internal/ui/screens_subs.go` (struct at line 22, `Footer` at 90, `rebuild` at 142, `Update` at 222)
- Modify: `README.md` (one short note: syncing needs `alass-cli` on `PATH`; place it where mpv is documented as a prerequisite)
- Test: `internal/ui/subs_sync_test.go`

**Interfaces:**
- Consumes: `subsync.Sync(ctx, refURL, subURL) (string, error)`, `subsync.ErrNoAlass`, `Player.AddLocalSubtitle`, existing `SubtitleAddedMsg`, `toast`, `toastErr`.
- Produces: new fields on `subsScreen`: `ref *stremio.Subtitle`, `syncing bool`, `syncCancel context.CancelFunc`, `synced map[string]string` (local path → originating list URL); new message `subSyncedMsg{id asyncID, sub stremio.Subtitle, path string, err error}`. `Footer()` includes `m` ("mark reference") and `S` ("sync to reference").

- [ ] **Step 1: Write the failing tests** in `subs_sync_test.go`, constructing `&subsScreen{...}` directly with a `listModel` from `newList()` and a few `stremio.Subtitle` values, then calling `Update(tea.KeyMsg{...})` (the early-out cases never touch the global `ctx`):
  - `TestMarkReferenceToggles`: `m` on row 0 sets `ref`; `m` again clears it.
  - `TestSyncWithoutReference`: `S` returns a command producing a `toastMsg` with `isErr` and text containing `mark a well-timed sub first`; `syncing` stays false.
  - `TestSyncRowIsReference`: with row 0 marked, `S` on row 0 yields the toast `pick a different sub to sync`.
  - `TestSyncIgnoredWhileRunning`: with `syncing = true`, `S` returns no command and does not replace `syncCancel`.
  - `TestSyncedMsgForStaleScreen`: a `subSyncedMsg` whose `id` differs from `s.id` changes nothing and returns no command.
  - `TestSyncedMsgError`: a `subSyncedMsg` with `err = subsync.ErrNoAlass` clears `syncing` and returns a toast containing `install alass-cli`; any other error shows the error text.
  - `TestAddedMsgForSyncedPathMarksOrigin`: with `synced = {"/tmp/x/out.srt": "http://a/sub1"}`, a `player.SubtitleAddedMsg{URL: "/tmp/x/out.srt"}` sets `s.active = "http://a/sub1"`.
  - `TestRebuildMarksReference`: after `m`, the badge of the marked row contains the marker text `ref`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/ui/ -run 'TestMark|TestSync|TestAddedMsg|TestRebuild' -v`
Expected: FAIL (build failure: unknown fields / message type).

- [ ] **Step 3: Implement.**
  - `m`: toggle `s.ref` to the highlighted entry (`s.subs[s.shown[i]]`) or nil; call `s.rebuild()`; `rebuild` adds a `ref` badge (use the existing `good(...)`/`grey(...)` style helpers) to the marked row.
  - `S`: early-out cases as in the tests (each via `toastErr`). Otherwise set `syncing = true`, create `ctx` with `context.WithCancel`, store the cancel func, and return a `tea.Batch` of `toast("syncing…")` and a command that calls `subsync.Sync` and returns `subSyncedMsg`. Name the package-level context-free variable carefully: the file already uses the global `ctx *appCtx`, so call the new local `cctx`.
  - `subSyncedMsg`: ignore when `m.id != s.id`; clear `syncing`; on error toast as tested; on success record `s.synced[m.path] = m.sub.URL`, set `s.pending = &m.sub`, and return `ctx.player.AddLocalSubtitle(m.path, langs.Name(m.sub.Lang), m.sub.Lang)`.
  - `SubtitleAddedMsg`: at the top of that case translate `m.URL` through `s.synced` (so the existing pending/active logic sees the origin URL); everything else in the case is unchanged.
  - Leaving the screen (`esc`, `backspace`, `q`) calls `syncCancel` when set. `R` (refetch) also assigns a new `s.id`, which makes any in-flight result stale.
  - Footer gains the two hints; README note added.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./... -race && go vet ./...`
Expected: PASS, no vet findings.

- [ ] **Step 5: Manual check in the real app.** Build (`make build`), play something, open the subtitle list, mark one sub with `m`, press `S` on another: expect the toast `syncing…`, then `subtitles on — <language>` and the synced row showing `✓ on`. Press `S` with no reference: expect the error toast.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/screens_subs.go internal/ui/subs_sync_test.go README.md
git commit -m "feat(ui): mark a reference subtitle and sync another to it"
```

---

### Task 5: mpv quick-sync key (dotfiles)

**Files:**
- Modify: `/home/kienct/.dotfiles/mpv/.config/mpv/scripts/autosubsync/autosubsync.lua` (new function near `sync_to_subtitle`, binding next to the existing `mp.add_key_binding` at the end of the file)
- Modify: `/home/kienct/.dotfiles/mpv/.config/mpv/input.conf` (after line 43)

**Interfaces:**
- Consumes: existing `get_loaded_tracks('sub')`, `sync_subtitles(ref_sub_path)`, `ref_selector`, `notify`.
- Produces: `local function sync_quick()` and the script binding `autosubsync-quick`.

Test harness (used by Tasks 5 and 6): `/tmp/claude-1000/-home-kienct-app-stremio-cliuwu/5a66d922-8007-4166-974d-4b06986e27d5/scratchpad/t/drive.py` starts a headless mpv (`--vo=null --ao=null --idle --no-terminal --log-file`) with your real config, loads a video and a subtitle over an IPC socket (use a short relative socket path, Unix socket paths are limited to 108 bytes), sends commands, and writes `mpv.log`. Test media in the same directory: `v.mkv` (20 s), `long.mkv` (40 min), `s.srt`, `s.vtt`. If those are gone, regenerate with `ffmpeg -f lavfi -i sine=...` as in the debugging session. Serve media with `python3 -m http.server <port>` to test URL playback.

- [ ] **Step 0: Baseline.** The dotfiles repo already has uncommitted fixes from the debugging session (`autosubsync.conf` absolute paths, stdout errors, WebVTT conversion). Commit them first as their own commit so this work diffs cleanly:

```bash
cd /home/kienct/.dotfiles
git add mpv/.config/mpv/script-opts/autosubsync.conf mpv/.config/mpv/scripts/autosubsync/autosubsync.lua
git commit -m "mpv: autosubsync absolute tool paths, alass stdout errors, WebVTT conversion"
```

- [ ] **Step 1: Write the failing check.** With the harness, load `v.mkv` over HTTP with two `sub-add` subs (`s.srt` and `s2.srt`, the second offset by a few seconds), send `script-binding autosubsync/autosubsync-quick`, and grep `mpv.log`. Expected before implementing: no `autosubsync` subprocess line, only the "script-binding ... not found" style failure.

- [ ] **Step 2: Run to verify it fails**

Run: `python3 drive.py ipc.sock http://127.0.0.1:8770/v.mkv http://127.0.0.1:8770/s.srt` (extended with a second `sub-add` and the quick binding), then `grep -E 'alass|Subtitle synchronized' mpv.log`
Expected: no output.

- [ ] **Step 3: Implement `sync_quick()`.** It takes the first track from `get_loaded_tracks('sub')` that is `external`, not `selected`, and passes `is_supported_format`; with none, `notify("No other external subtitle to sync to.", "error", 4)` and return. Otherwise set `ref_selector.selected = 2` (so `engine_selector:get_engine_name()` returns the configured `altsub_subsync_tool`) and call `sync_subtitles(track['external-filename'])`. Register `mp.add_key_binding(nil, "autosubsync-quick", sync_quick)`. Add `Alt+n script-binding autosubsync/autosubsync-quick` to `input.conf` under the existing "Subtitle sync" block with a comment.

- [ ] **Step 4: Run to verify it passes.** Same harness run. Expected `mpv.log` lines: `Starting alass...` with the second sub as reference, then `Subtitle synchronized.`. Second run with only one external sub: the log shows `No other external subtitle to sync to.` and no `alass` line.

- [ ] **Step 5: Commit**

```bash
cd /home/kienct/.dotfiles
git add mpv/.config/mpv/scripts/autosubsync/autosubsync.lua mpv/.config/mpv/input.conf
git commit -m "mpv: one-key sync of the active subtitle to another external one"
```

---

### Task 6: mpv audio sync from the first 10 minutes (dotfiles)

**Files:**
- Modify: `/home/kienct/.dotfiles/mpv/.config/mpv/scripts/autosubsync/autosubsync.lua` (`ref_selector` items and index handling at lines ~415-540; `sync_subtitles` signature)

**Interfaces:**
- Consumes: `sync_subtitles`, `subprocess`, `os_temp`, `get_active_track('audio')`, `config.ffmpeg_path`, `config.alass_path`.
- Produces: `sync_subtitles(ref_sub_path, opts)` where `opts` is an optional table `{ engine = "alass", alass_args = { "--no-split" } }`; when `opts.engine` is set it replaces `engine_selector:get_engine_name()` and `opts.alass_args` are inserted before the file arguments of the `alass` call. New function `local function sync_audio_clip()`. `ref_selector.items` becomes `{ 'Sync to audio', 'Sync to another subtitle', 'Sync to audio (first 10 min)', 'Save current timings', 'Cancel' }`, and every index check in `get_ref`, `get_subsync_tool`, `act`, `call_subsync` shifts so item 3 is the clip sync, 4 is manual timings and 5 is cancel.

- [ ] **Step 1: Write the failing check.** With the harness and `long.mkv` (40 min) served over HTTP, load it with `s.srt` (subtitles shifted by a known offset relative to the audio), open the menu and select the third item (`keypress DOWN` twice, then `ENTER`). Expected before implementing: item 3 is "Save current timings" and the log shows the manual-offset message instead of an audio clip run.

- [ ] **Step 2: Run to verify it fails**

Run: harness with the menu keypresses, then `grep -E 'autosubsync_clip|--no-split' mpv.log`
Expected: no output.

- [ ] **Step 3: Implement.**
  - `sync_audio_clip()`: run `ffmpeg -hide_banner -nostdin -y -loglevel quiet -t 600 -i <video path> -vn -map 0:a:<n> -ac 1 -ar 16000 <os_temp()>autosubsync_clip.wav` (`<n>` is the zero-based index of the active audio track among audio tracks, derived from `get_active_track('audio')`); on failure `notify("Couldn't extract audio clip.", "error", 5)`. Then call `sync_subtitles(clip_path, { engine = "alass", alass_args = { "--no-split" } })` and delete the clip afterwards with `os.remove`.
  - Menu item 3 calls `sync_audio_clip()` directly, bypassing the engine and track selectors. Add a one-line notice before starting: `Syncing against the first 10 minutes only (constant offset).`
  - `sync_subtitles` applies `opts` as described in Interfaces; with `opts == nil` behaviour is unchanged.

- [ ] **Step 4: Run to verify it passes.** Same harness run. Expected `mpv.log`: an `ffmpeg` subprocess line containing `-t 600`, then an `alass-cli` line containing `--no-split` and the clip path, then `Subtitle synchronized.`; the retimed `.srt` in `/tmp/` is offset by the expected amount (compare first cue time with the original). Also confirm the clip was read partially: the HTTP server log shows requests, and `autosubsync_clip.wav` no longer exists afterwards. Then re-run the existing "Sync to audio" (item 1) on `v.mkv` and confirm it still works.

- [ ] **Step 5: Commit**

```bash
cd /home/kienct/.dotfiles
git add mpv/.config/mpv/scripts/autosubsync/autosubsync.lua
git commit -m "mpv: add audio sync from the first 10 minutes"
```

---

## Self-review

- **Spec coverage:** WebVTT conversion, fetch/cap/timeout, alass lookup and stdout errors, typed errors, temp dir + cleanup, `AddLocalSubtitle` path restriction, `m`/`S` keys with all four error cases, marker on the originating row, non-persisted reference, quick-sync key, 10-minute clip with `--no-split`, the error-handling table, and the testing section each map to a task above. "Sync cancelled (leaving the screen)" is Task 4. README note is in Task 4.
- **Type consistency:** `Sync`, `Owns`, `Cleanup`, `ErrNoAlass`, `ErrTooLarge`, `AddLocalSubtitle`, `subSyncedMsg` and the `sync_subtitles(ref, opts)` signature are named identically wherever they appear.
- **Known risk:** Task 4's `ctx` name clashes: the screen file uses the global `ctx *appCtx`, so the cancellable context there is named `cctx`.
