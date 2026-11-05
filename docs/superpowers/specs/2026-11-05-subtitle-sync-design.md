# Subtitle sync

Date: 2026-11-05
Status: design approved, implementation plan not yet written

## Problem

Subtitles from addons are often mistimed for the file being played. Syncing a
subtitle to the audio with `alass` or `ffsubsync` (the mpv `autosubsync`
script) makes the tool read the whole video. With a TorBox cached link
(a multi-GB HTTPS URL) that is a second full download, with no progress
display and no timeout.

Two failures were found while debugging this in the existing mpv setup:

- `alass-cli` writes its errors to stdout, so the script's "Subtitle
  synchronization failed." message was empty. Already fixed in the dotfiles.
- Addons serve WebVTT under a `.srt` name. `alass` picks its parser from the
  extension and fails on the `WEBVTT` header. Already fixed in the dotfiles
  (the script converts with ffmpeg).

## Goal

Get a correctly timed subtitle in about a second, without reading the video.

Success criteria:

- In the app's subtitle list, a mistimed subtitle can be synced to a
  well-timed one with two key presses and no video download.
- In mpv, the same sync is one key press, with no menu.
- When no well-timed subtitle exists, an audio sync that reads about a
  quarter of a 40-minute file is available as a fallback.

## Scope

Three pieces, in two places:

| # | Piece | Where |
|---|---|---|
| 1 | Sync a picked subtitle to a reference subtitle | stremio-cliuwu (Go) |
| 2 | One-key "sync active sub to another sub track" | mpv dotfiles |
| 3 | Audio sync from the first 10 minutes only | mpv dotfiles |

Piece 3 is not in the app. It would need ffmpeg as a new dependency there.

Not included: remembering the reference per show, an automatic mode that
syncs every pick, syncing against embedded tracks (see Constraints).

## Constraints

- `alass` stays the engine. `alass-cli` must be on the user's `PATH`; the app
  does not install it.
- An embedded subtitle track cannot be extracted cheaply from a URL: ffmpeg
  has to read through the whole file. So the reference subtitle must be one
  from the addon list (an external subtitle), never an embedded track.
- `Player.AddSubtitle` accepts only `http(s)` URLs on purpose: mpv fetches
  them itself, and the app does not hand mpv local paths on a remote's say-so.
  Loading a synced file therefore needs a separate, narrower entry point.
- `alass` supports SubRip, SubStationAlpha (`.ssa`/`.ass`) and VobSub only.
  Anything else, including WebVTT, must be converted first.

## Design

### 1. App: `internal/subsync` and the subtitle list

New package `internal/subsync`. The UI calls one function; the package owns
fetching, normalising and aligning.

```go
// Sync downloads ref and sub, normalises both to SRT, aligns sub to ref with
// alass, and returns the path of the retimed file.
func Sync(ctx context.Context, refURL, subURL string) (outPath string, err error)
```

Steps inside `Sync`:

1. **Fetch** both URLs with `http.Client` (30 s timeout, `ctx` cancel). Cap
   each body at 4 MB (`io.LimitReader`); a larger body is an error.
2. **Normalise to SRT** with `ToSRT(data []byte) ([]byte, error)`:
   - Strip a UTF-8 BOM.
   - If the data starts with `WEBVTT`, convert it in plain Go: drop the
     header, `NOTE` and `STYLE` blocks; drop cue identifiers; take only the
     `start --> end` part of timing lines (dropping cue settings); change
     `.` to `,` in the timestamps; add the hour field when it is missing
     (`MM:SS.mmm`); strip `<...>` tags; number the cues.
   - Otherwise return the data unchanged (SubRip and ASS pass through).
3. **Align**: write both to a per-session temp directory and run
   `alass-cli ref.srt sub.srt out.srt`. The binary is found with
   `exec.LookPath("alass-cli")`, then `exec.LookPath("alass")`. Run with the
   context so a cancelled sync kills the process. Collect stdout and stderr
   together: `alass-cli` reports errors on stdout.
4. Return the path of `out.srt`.

Errors are typed so the UI can word them:

- `ErrNoAlass`: neither binary found.
- `ErrTooLarge`: body over the cap.
- fetch and alass failures are wrapped with the last non-empty line of the
  tool's output (without the `RUST_BACKTRACE` hint).

Temp directory: `os.MkdirTemp("", "stremio-cliuwu-subs-")` created on first
use, removed when the app exits. Output files in it are the only files
`AddLocalSubtitle` accepts.

**Player change** (`internal/player/player.go`):

```go
// AddLocalSubtitle loads a file the app generated into the running video.
// path must be inside the app's own subtitle temp directory.
func (p *Player) AddLocalSubtitle(path, title, lang string) tea.Cmd
```

It rejects any path that is not inside the subsync temp directory (after
`filepath.Clean` and a prefix check), then sends `sub-add <path> select
<title> <lang>` and returns the existing `SubtitleAddedMsg`. For that message
`URL` carries the path, so the list's "which track is playing" marker needs a
way to match it: the screen remembers which list entry the synced file came
from and marks that entry active.

**UI** (`internal/ui/screens_subs.go`):

- New field `ref *stremio.Subtitle` on `subsScreen`.
- `m` on the highlighted row marks it as the reference. Pressing `m` on the
  marked row clears it. The marked row shows a marker in the list and the
  footer hint lists `m` and `S`.
- `S` on the highlighted row syncs it to the reference:
  - no reference marked: toast "mark a well-timed sub first (m)".
  - highlighted row is the reference: toast "pick a different sub to sync".
  - otherwise: toast "syncing…", run `subsync.Sync` as a `tea.Cmd`, then
    `AddLocalSubtitle`, then toast "subtitles on — synced".
  - failures: toast with the wrapped error (for `ErrNoAlass`:
    "alass not found — install alass-cli"). The list stays open.
- The reference is not persisted. It lasts as long as the subtitle screen.
- A synced pick does not change `sub_prefs.json` beyond what a normal hand
  pick of that language already does.
- Normal pick (Enter) is unchanged.

### 2. mpv script: one-key sync (dotfiles)

In `scripts/autosubsync/autosubsync.lua`, a new function and binding
`autosubsync-quick`. It syncs the active subtitle track to the first other
external subtitle track (by track id order). If there is no other external
subtitle, it shows an error message and does nothing.

It reuses `sync_subtitles(ref)`, which already downloads URL subtitles, converts
WebVTT, and shows `alass`'s stdout on failure.

Binding in `input.conf`: `Alt+n script-binding autosubsync/autosubsync-quick`.
(`n` belongs to `autosub.lua` and `N` opens the existing menu.)

### 3. mpv script: audio sync from the first 10 minutes (dotfiles)

The existing menu gets an item "Sync to audio (first 10 min)", next to
"Sync to audio".

1. `ffmpeg -nostdin -y -loglevel quiet -t 600 -i <path> -vn -map 0:a:<n> -ac 1
   -ar 16000 <temp>/autosubsync_clip.wav`, where `<n>` is the active audio
   track. `-t 600` stops reading after 10 minutes of output, so only about a
   quarter of a 40-minute file is read.
2. `alass-cli --no-split <clip.wav> <sub> <out>`. `--no-split` applies one
   constant offset to the whole subtitle. The sub is longer than the clip, so
   split detection would only fit the first 10 minutes.
3. Delete the clip afterwards.

Limitation, stated in the menu hint: this finds a constant offset only; drift
later in the file is not corrected.

## Error handling

| Case | Behaviour |
|---|---|
| `alass-cli` missing | App toast / script message naming the tool |
| Subtitle download fails or exceeds 4 MB | Error toast; list stays open |
| WebVTT that fails to convert | Error naming the parse problem; no sync |
| `alass` exits non-zero | Last non-empty output line is shown, not a bare "failed" |
| Sync cancelled (leaving the screen) | Context cancel kills `alass` |
| `AddLocalSubtitle` given a path outside the temp dir | Rejected without calling mpv |

## Testing

App (Go):

- `ToSRT`: table tests with a BOM, cue ids, `NOTE`/`STYLE` blocks, cue
  settings, tags, `MM:SS.mmm` timestamps, an already-valid SRT (unchanged),
  and empty input.
- `Sync`: against an `httptest` server, with a fake `alass-cli` shell script
  on a temp `PATH` that copies its input to the output, and a second fake that
  prints an error to stdout and exits 1 (checks the error text is surfaced).
  A case with no binary on `PATH` expects `ErrNoAlass`.
- `AddLocalSubtitle`: path inside the temp dir accepted; `..` traversal,
  absolute outside paths and symlinks rejected.
- `subsScreen`: key handling for `m` and `S` for the no-reference and
  same-row cases, using the existing screen test style.

Script: verified with a headless mpv driven over the IPC socket, as in the
debugging session (`--vo=null --ao=null`, `sub-add` of a URL subtitle, then
the binding). Cases: quick sync with two external subs, quick sync with one
(expects the error message), and the 10-minute audio sync against a generated
40-minute file.

## Open items

None. Defaults chosen during design: key `m` marks the reference, `S` syncs,
`Alt+n` is the mpv quick-sync key; the 4 MB cap, 30 s timeout and 600 s clip
length are fixed constants, not settings.
