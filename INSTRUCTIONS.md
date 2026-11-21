# PyCloud — Maintainer Instructions

Internal notes for developing, debugging, and releasing PyCloud. For the user-facing guide, see [README.md](README.md).

**Contents**

- [Repo layout](#repo-layout)
- [Prerequisites](#prerequisites)
- [First-time setup](#first-time-setup)
- [Local development loop](#local-development-loop)
- [Testing](#testing)
- [Debugging](#debugging)
- [Frontend notes](#frontend-notes)
- [Where things live at runtime](#where-things-live-at-runtime)
- [Releasing](#releasing)
- [Publishing release binaries](#publishing-release-binaries)
- [Regenerating screenshots](#regenerating-screenshots)
- [Reset and repair recipes](#reset-and-repair-recipes)
- [Known rough edges](#known-rough-edges)
- [Release checklist](#release-checklist)

---

## Repo layout

| Path | What it is |
|---|---|
| `main.go` | Entry point: config load, route table, middleware pipeline, TLS, server lifecycle |
| `config.go` | `Config` struct, defaults, file/env/flag precedence, `-set-password` |
| `middleware.go` | Logging, security headers + CSP, CSRF, session auth |
| `auth_handlers.go` | `status`, `setup`, `login`, `logout` |
| `files.go` | List, upload, download, delete, rename, mkdir |
| `media.go` | Stream, subtitle, and media-info endpoints |
| `tasks.go` | yt-dlp task manager, worker pool, install/status endpoints |
| `stats.go`, `stats_unix.go`, `stats_windows.go` | Telemetry collector (per-OS) |
| `security.go`, `security_test.go` | PBKDF2, tokens, path jail, XSS/URL validation |
| `tls.go` | Self-signed certificate generation |
| `server_test.go` | End-to-end HTTP tests |
| `frontend/` | The entire web client — embedded into the binary |
| `frontend/js/app.js` | All client logic (navigation, API, UI) |
| `frontend/js/theme.js` | Pre-paint theme resolution (loaded in `<head>`) |
| `frontend/css/style.css` | Single stylesheet with theme tokens |
| `frontend/fonts/`, `frontend/favicon.svg` | Self-hosted assets |
| `assets/` | README screenshots |
| `Makefile` | `build`, `test`, `run`, `release`, `clean` |
| `config.example.json` | Starting point for `config.json` |
| `secure_server.py` | **Legacy** Python server (pre-v2.0). Not part of the Go build. |
| `newchanges/` | UI design references and planning docs. Not part of the app. |

Frontend files are loaded into the binary with `//go:embed frontend/*` (see `main.go`).

## Prerequisites

- **Go 1.27+** (`go.mod` pins `go 1.27.4`). Confirm with `go version`.
- **GNU make** (optional — every target is a thin `go build`/`go test` wrapper).
- **yt-dlp** and **ffmpeg** on `PATH` *only* if you want to exercise the "Fetch media" flow locally. Not needed for the rest of the app.
- A modern browser for UI verification.

PyCloud has **zero third-party Go dependencies** — no `go.sum`, no vendoring. Keep it that way unless there is a strong reason.

## First-time setup

```bash
git clone https://github.com/Gaurav5189/PyCloud-Server.git
cd PyCloud-Server

# 1. Create a config with a known test password (writes ./config.json and exits)
go run . -set-password 'testpassword123'

# 2. Run it (add -no-tls to avoid the self-signed certificate during dev)
go run . -no-tls -port 8443 -root ./storage
```

Open <http://localhost:8443> and sign in with the password you set.

`config.json`, `certs/`, and `storage/` are all git-ignored. `-set-password` requires the process to write `config.json` in its working directory, so run it from the repo root or point `PYCLOUD_CONFIG` elsewhere.

## Local development loop

**Important:** the frontend is embedded at compile time. Editing anything under `frontend/` has no effect until you rebuild and restart.

```bash
make build && ./pycloud -no-tls -port 8443     # ~2–3s on a laptop
```

`make run` does the same thing in one step. Hard-refresh the browser after a restart if you changed CSS.

> **Optional faster loop:** keep the Go server running and put a dev reverse proxy in front that serves `frontend/` from disk, so static edits reload without a rebuild:
>
> ```bash
> # terminal 1
> ./pycloud -no-tls -bind 127.0.0.1 -port 8443
> ```
>
> ```caddyfile
> # terminal 2 — save as Caddyfile, then run: caddy run
> :8080 {
>     handle_path /static/* {
>         root * frontend
>         file_server
>     }
>     handle {
>         reverse_proxy 127.0.0.1:8443
>     }
> }
> ```
>
> Browse `http://localhost:8080`. API calls still reach the Go server; only static assets come from disk. Remember to run a normal `make build` before testing the real embedded build.

### Verifying that assets are embedded

```bash
grep -c "PlexSans-latin" ./pycloud      # > 0  → fonts are in the binary
grep -c "notice-stack"  ./pycloud      # > 0  → latest index.html/CSS are in the binary
```

## Testing

```bash
go vet ./...            # static checks
go test ./...           # full suite
go test -race ./...     # adds the race detector (slower)
go test -run TestLoginAndAuthProtection -v ./...
```

The suite covers password hashing and verification, the path jail, session management, the status/setup/login flow, upload deduplication, and yt-dlp URL validation. When you add an endpoint, add a case to `server_test.go` (it spins up the real handler stack).

## Debugging

**Request logging.** Every request is logged by `LoggingMiddleware` with method, path, status, duration, and bytes written. This is your first stop:

```
2026/01/01 12:00:00 [127.0.0.1] POST /api/upload 200 12ms 45 bytes
```

**Port already in use.** `ss -ltnp | grep 8443` (Linux) or `lsof -i :8443` (macOS), then kill the stale process or use `-port`.

**TLS problems.** A broken or stale certificate lives in `certs/`. Delete `certs/server.crt` and `certs/server.key` and restart to regenerate the self-signed pair. For local work, prefer `-no-tls`.

**Locked out.** Delete `config.json` (or blank `password_hash`) and restart to return to the setup screen. Files are untouched. `PYCLOUD_PASSWORD=... ./pycloud` also resets it quickly.

**Exercising the download queue without real downloads.** Point `yt_dlp_path` at a stub that mimics yt-dlp's flags and writes a file:

```bash
cat > /tmp/yt-dlp-stub <<'SH'
#!/bin/bash
out=""
while [ $# -gt 0 ]; do
  case "$1" in -o) out="$2"; shift 2;; *) shift;; esac
done
echo "[download] Destination: stub"
sleep 0.5; echo "[download] 100% of 1.00MiB"
[ -n "$out" ] && head -c 2048 /dev/urandom > "$(printf '%s' "$out" | sed 's/%(title)s/stub_video/; s/%(ext)s/mp4/')"
exit 0
SH
chmod +x /tmp/yt-dlp-stub
```

Then set `"yt_dlp_path": "/tmp/yt-dlp-stub"` in `config.json` and restart. `/api/tasks/ytdlp-status` reports it as installed, so you can test queueing, logs, cancel, and retry without network access.

**Frontend errors.** Open the browser console. The strict CSP (`script-src 'self'`) means inline scripts and CDNs are blocked by design — all JavaScript must live in `frontend/js/*.js`. If a request 401s, the client shows the login dialog; a 428 shows the setup dialog.

**Storage sanity.** The jail refuses paths outside `root_dir`. If an operation 403s, check `SafePathJail` and confirm the path is relative to the storage root and not a symlink pointing out of it.

## Frontend notes

- **No build step.** Plain ES2020 JavaScript, one stylesheet, no bundler, no npm. Keep it that way.
- **No `innerHTML`.** Server and filesystem values are rendered with `textContent`/`createElement` to avoid XSS. Follow that pattern.
- **Theming.** Colors are CSS custom properties defined per `html[data-theme="…"]` block in `style.css` (`graphite`, `paper`, `terminal`). To add a theme, copy a token block and add a button to `#themePicker` in `index.html`. `theme.js` resolves the saved choice before first paint to avoid a flash.
- **Icons** live in the inline SVG sprite at the top of `index.html` (`<symbol id="i-name">`). Reference them with `<use href="#i-name">`.
- **Adding a view** means: a `<section data-view="x">` in `index.html`, an entry in the `VIEW_TITLES` map in `app.js`, a nav link with `data-nav="x"`, and a branch in `renderView`/`onViewEnter`.
- **Responsive breakpoints** are at 1180px (icon rail), 1020px (2-column stats), 900px (off-canvas sidebar), 760px, 640px (bottom-sheet dialogs / stacked file rows), and 420px.

## Where things live at runtime

| Path | Notes |
|---|---|
| `config.json` | Secrets (`password_hash`, `session_secret`). Never commit. |
| `certs/` | Auto-generated TLS material. Safe to delete (regenerates). |
| `<root_dir>/media/uploads` | Default upload destination |
| `<root_dir>/media/yt_library` | Destination for fetched media |
| `<root_dir>/.pycloud_trash` | Deleted items land here as `<unix>_<name>` |

Deleting from inside `.pycloud_trash` erases the item permanently — there is no restore endpoint.

## Releasing

`make release` runs the tests, wipes `dist/`, then cross-compiles five targets:

```bash
make release
```

```
dist/pycloud-linux-amd64
dist/pycloud-linux-arm64
dist/pycloud-windows-amd64.exe
dist/pycloud-darwin-arm64
dist/pycloud-darwin-amd64
```

It also runs `clean`, which deletes the root `./pycloud` binary and the whole `dist/` directory — so run `make build` afterwards if you want a local `./pycloud`.

Builds are pure Go (`CGO_ENABLED=0`), so no cross-compilers are needed. Every binary embeds the frontend **as it exists on disk at build time** — always rebuild after a frontend change.

Smoke-test a release binary before publishing:

```bash
dist/pycloud-linux-amd64 -no-tls -port 8444 -root /tmp/pycloud-smoke
# then check http://localhost:8444: log in, upload a file, open Media, open Fetch media
```

## Publishing release binaries

`dist/` is listed in `.gitignore`, so fresh binaries are **not** part of the repo and links into `dist/` on GitHub will 404 until you publish them. Two options:

**Option A — GitHub Release (recommended).** Keeps the repo small and gives versioned, cacheable downloads:

```bash
git tag v2.0.0
git push origin v2.0.0
gh release create v2.0.0 dist/* \
  --title "PyCloud v2.0.0" \
  --notes "Single static binary for Linux, macOS, and Windows."
```

The README's download commands point at `raw.githubusercontent.com/.../main/dist/…`. On a Release the equivalent URLs are `https://github.com/Gaurav5189/PyCloud-Server/releases/download/v2.0.0/<file>`. If you switch fully to Releases, update the README's `Installation` section to match.

**Option B — commit the binaries.** Makes the README's `dist/` links work as written, at the cost of repo size (~40 MB for all five):

```bash
make release
git add -f dist/
git commit -m "Publish v2.0.0 release binaries"
git push
```

Either way, run `make release` first so the published binaries contain the current frontend.

## Regenerating screenshots

The README embeds `assets/image01.png` (Overview) and `assets/image02.png` (Media). They go stale whenever the UI changes, so refresh them as part of a release that touches the frontend:

1. Start a build against a storage root that already has a few videos, images, and audio files.
2. Set the viewport to **1440×900**, choose the **Graphite** theme, and let the page finish loading (dismiss toasts).
3. Capture the **Overview** view into `assets/image01.png` and the **Media** view into `assets/image02.png`.

With Playwright available, the equivalent is:

```js
await page.setViewportSize({ width: 1440, height: 900 });
await page.goto('http://localhost:8443/#overview');
await page.waitForTimeout(3000);
await page.screenshot({ path: 'assets/image01.png' });
await page.goto('http://localhost:8443/#media');
await page.waitForTimeout(3000);
await page.screenshot({ path: 'assets/image02.png' });
```

Keep the files at those exact paths so the README keeps working.

## Reset and repair recipes

```bash
# Re-run first-time setup
rm config.json

# Regenerate TLS certificates
rm -rf certs

# Start over completely (destroys stored files)
rm -rf config.json certs storage .pycloud_trash

# Find what is holding a port
ss -ltnp | grep 8443
```

## Known rough edges

- `trash_retention_days` exists in `Config` but nothing reads it. Trash is manual. Either wire up a cleanup goroutine or drop the key; do not claim automatic purging in docs until then.
- `newchanges/` and `secure_server.py` are reference/legacy files. If they are not meant to ship, remove them from the repo rather than leaving them ambiguous.
- `-set-password` does not enforce the 8-character minimum that the web setup screen does. Hash length is unchecked by design (it calls `HashPassword` directly).
- Task history is capped at 100 entries with a fixed 2-hour per-job timeout; both are hard-coded in `tasks.go`.

## Release checklist

- [ ] `go vet ./...` and `go test ./...` pass (plus `-race` for anything concurrency-related)
- [ ] `frontend/` builds and renders: log in, upload, delete to Trash, play a video with subtitles, queue a fetch, switch all three themes
- [ ] Checked the layout at 1440 / 1024 / 820 / 390 px
- [ ] `ServerVersion` in `main.go` bumped and consistent with the tag
- [ ] README reviewed for drift (flags, config keys, endpoints, screenshot freshness)
- [ ] `make release` run against the final commit
- [ ] Binaries published (Release or `git add -f dist/`) and the download URLs verified with `curl -fL -o /dev/null <url>`
