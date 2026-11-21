# PyCloud v8.6 — Redesign Design Specification

| | |
|---|---|
| **Project** | PyCloud v8.6 — self-hosted personal cloud, file manager, media center & task engine |
| **Deliverable** | Full UI/UX redesign of the web dashboard (`secure_server.py` front end) |
| **Status** | Concept prototype approved for review: [`pycloud-redesign.html`](./pycloud-redesign.html) (single file, zero dependencies, dark + light themes) |
| **Design language** | "Quiet chrome" — dark-first, high information density, keyboard-first operation |
| **Inspiration** | Linear · Vercel Geist · Raycast (see §15 References) |
| **Audited evidence** | `image01.png` (current dashboard), `image02.png` (current media view + Fetch modal) |

---

## 1. Project Overview

PyCloud is a single-process Python application — "No frameworks. No database. One Python script." — serving a personal cloud for VPS / Raspberry Pi / home-lab / local Linux machines. It provides:

- a **live system dashboard** (CPU / RAM / disk),
- **file management** (drag & drop upload, rename, delete — folders use irreversible `shutil.rmtree` "nuclear delete"),
- **media streaming** (Cinema mode for MP4/MKV/WebM, auto-loading `.vtt` subtitles, >400 MB files fall back to download mode),
- a **background task engine** (sequential `yt-dlp` queue with live logs, cancel/retry),
- **auto-categorization** of files into *Manual Uploads*, *YouTube Library*, *Old Root Files*.

The current UI is a functional but dated single-column page. This redesign rebuilds the entire experience around a modern application shell while preserving PyCloud's core philosophy: **zero build step, zero framework dependencies, one deployable artifact**.

**Scope of the redesign (all present in the prototype):**

1. App shell — sidebar navigation, sticky topbar, 5 views (Overview, Files, Media, Tasks, Settings)
2. Overlays — Cinema/Audio player, command palette, Fetch-Video modal, confirm dialogs, split-screen login, toast system
3. Design system — dual themes, 60+ components, motion language, iconography
4. Responsive strategy — desktop → icon rail → mobile drawer

---

## 2. Current State Audit (Before)

Source: the two uploaded screenshots, analyzed component by component.

### 2.1 Structural findings

| # | Finding | Severity |
|---|---|---|
| S1 | No navigation at all — a single scrolling page; "Logout" is the only global control. Users cannot tell where they are or jump between areas | Critical |
| S2 | No layout grid or max-width container; content stretches edge-to-edge on wide screens, producing unreadable line lengths | High |
| S3 | Floating action button (maroon circle) overlaps the last file row and occludes its delete button | High |
| S4 | Vertical rhythm random — uneven 40–60 px section gaps vs. cramped table rows | Medium |
| S5 | File lists are flat, filename-only: no file-type icons, no thumbnails, no column structure, no ellipsis truncation for long underscore-laden names | High |
| S6 | Native unstyled `<input type="file">` ("Browse… / No file selected") inside the dropzone | High |

### 2.2 Visual findings

| # | Finding | Severity |
|---|---|---|
| V1 | Emoji used as the icon system (🔥 📁 ✏️ 🔒 ❌ 📺 📦) — renders inconsistently across platforms, unprofessional | High |
| V2 | Six unrelated accent colors used simultaneously (blue, teal, green, red, maroon, neon cyan) with no semantic system | High |
| V3 | Grey-gradient "Close" button (2010s pattern) beside a flat neon-cyan CTA in the same modal — two conflicting button styles | Medium |
| V4 | Metadata contrast failure: dim grey `#5f5f5f` on `#161616` measures **2.83:1** (WCAG minimum 4.5:1) | High |
| V5 | Status color ("COMPLETED" green) identical to data-viz color (disk green) — no separation of semantics | Medium |
| V6 | Inconsistent data formatting (CPU value "68.31" has no unit, RAM does) | Low |
| V7 | Three competing headline scales (logo, section header, stat values) with no clear hierarchy | Medium |
| V8 | Static stat cards — numbers only, no history/sparklines despite the server already exposing live metrics | Medium |
| V9 | No empty states, no breadcrumbs, no sort/filter affordances beyond a raw text filter | Medium |

### 2.3 UX findings

| # | Finding | Severity |
|---|---|---|
| U1 | Destructive folder deletion ("nuclear delete") has **no confirmation step** in the UI | Critical |
| U2 | No undo affordance for accidental file deletion | High |
| U3 | No keyboard support, no command access — every action is a mouse hunt | High |
| U4 | Upload progress is a bare bar; no filename, no percentage, no cancel | Medium |
| U5 | Task logs hidden behind a raw "Logs" button with no live-feed affordance | Medium |

---

## 3. Design Direction

### 3.1 Positioning statement

> PyCloud is a **server operator's cockpit**, not a consumer file-sharing page. The UI should feel like a modern developer tool — quiet, dense, dark, keyboard-first — where live system data, running tasks and large media libraries are all one keystroke away.

### 3.2 Design principles

1. **Quiet chrome, loud content** — backgrounds, borders and panels stay near-invisible (1 px hairlines, low-alpha fills) so files, metrics and task state carry the color.
2. **One accent to rule them all** — a single cyan→blue gradient is the brand; every other hue is reserved for semantics (file types, status) and never decorates.
3. **Density without clutter** — 13–13.5 px UI text, mono for all machine data (sizes, speeds, percentages, IDs, paths), tight 10 px component gaps, generous 14–18 px section gaps.
4. **Everything is one keystroke away** — `Ctrl K` opens a command palette covering navigation, actions and file search.
5. **Destructive power demands ceremony** — nuclear folder delete gets a typed-confirmation-style modal; simple file delete gets an instant Undo toast instead of a nagging dialog.
6. **Honest liveness** — real-feeling data cadence (1.4 s stat ticks, pulsing status dots, shimmering progress) so the operator can trust what they see.

### 3.3 What was borrowed from where

| Reference | Borrowed |
|---|---|
| **Linear** (linear.app) | Sidebar + view shell, hairline borders, quiet hover states, keyboard-first model, chip filters |
| **Vercel Geist** (vercel.com/geist) | Dark-first token architecture, Inter + JetBrains Mono pairing, mono numerals for metrics, subtle radial background glows |
| **Raycast** (raycast.com) | Command palette pattern (⌘K), blurred overlay scrims, gradient glow accents on dark surfaces |

---

## 4. Design Tokens

All tokens are CSS custom properties defined on `:root` (dark default) and overridden under `html[data-theme="light"]`. This is the single source of truth — every component consumes tokens only, never raw hex values.

### 4.1 Color — Dark theme (default)

| Token | Value | Usage |
|---|---|---|
| `--bg` | `#0B0E14` | Page background |
| `--bg-2` | `#0D1119` | Sidebar background |
| `--card` | `#10141D` | Card / panel surface |
| `--card-2` | `#141927` | Elevated surface (hover) |
| `--inset` | `#0B0F16` | Input wells, segmented controls |
| `--line` | `rgba(148,163,184,.10)` | Hairline borders (1 px) |
| `--line-2` | `rgba(148,163,184,.20)` | Emphasized borders (inputs, hover) |
| `--t1` | `#EEF2F8` | Primary text |
| `--t2` | `#96A1B7` | Secondary text |
| `--t3` | `#5D6A80` | Tertiary / metadata text (non-essential only — see §10) |
| `--cyan` | `#22D3EE` | Brand accent (links, focus, live) |
| `--blue` | `#3B82F6` | Gradient end stop |
| `--grad` | `linear-gradient(135deg, #22D3EE 0%, #3B82F6 100%)` | Brand gradient — primary buttons, active nav, progress fills, logo |
| `--grad-soft` | same hues @ 16 % alpha | Soft fill — active states, icon wells |
| `--ok` / `--ok-bg` | `#34D399` / 12 % alpha | Success, online, audio files |
| `--warn` / `--warn-bg` | `#FBBF24` / 12 % alpha | Warnings |
| `--bad` / `--bad-bg` | `#F87171` / 12 % alpha | Errors, destructive, cancelled |
| `--vio` / `--vio-bg` | `#A78BFA` / 14 % alpha | Video files, RAM metric |
| `--pink` / `--pink-bg` | `#F472B6` / 14 % alpha | Image files, YouTube category |
| `--amber` / `--amber-bg` | `#FBBF24` / 14 % alpha | Archive files, disk metric |
| `--top-glass` | `rgba(11,14,20,.78)` | Topbar glass surface (blur 14 px) |
| Log console | `#05070C` bg, `#8BE9B8` text | Terminal aesthetic stays dark in both themes |

Hues follow the Tailwind palette at the **400** step (dark) and **600/700** step (light) so both themes share identical hue families.

### 4.2 Color — Light theme

| Token | Value | Change vs. dark |
|---|---|---|
| `--bg` | `#F3F5F9` | Cool paper, not pure white |
| `--bg-2` | `#EDF0F6` | |
| `--card` | `#FFFFFF` | Cards float on paper |
| `--card-2` | `#F7F9FC` | |
| `--inset` | `#F1F4F9` | |
| `--line` / `--line-2` | `#E2E7F0` / `#CBD3E1` | Solid, not alpha (avoid muddy greys) |
| `--t1` / `--t2` / `--t3` | `#0F1A2E` / `#4E5D75` / `#8B97AB` | |
| `--cyan` / `--blue` | `#0891B2` / `#2563EB` | Darkened one step for contrast on white |
| `--ok` / `--warn` / `--bad` / `--vio` / `--pink` | `#059669` / `#B45309` / `#DC2626` / `#7C3AED` / `#DB2777` | All darkened to 600/700 |
| Shadows | `0 1px 2px rgba(15,26,46,.06), 0 12px 32px -16px rgba(15,26,46,.14)` | Much softer |
| Glow opacity | `--glow-o:.35` (vs `.55` dark) | Background aurora restrained |

The player, log console and login brand panel intentionally **stay dark in both themes** — cinema and terminal surfaces are always night surfaces.

### 4.3 Typography

| Role | Family | Weights |
|---|---|---|
| UI / text | **Inter** (Google Fonts, system fallback `-apple-system, Segoe UI, system-ui`) | 400 / 500 / 600 / 700 / 800 |
| Machine data (sizes, %, speeds, IDs, paths, logs, timestamps, versions) | **JetBrains Mono** | 400 / 500 / 600 |

**Type scale (px, tight tracking on headings):**

| Style | Spec | Where |
|---|---|---|
| Display | 31 / 800 / −0.03em | Login headline (gradient-clipped `<em>`) |
| Stat value | 26 / mono 600 / −0.03em | Dashboard stat cards |
| Page title (h1) | 21 / 700 / −0.02em | Each view's `page-head` |
| Media hero title | 19 / 700 / −0.02em | Continue-watching hero |
| Topbar title | 15.5 / 700 | Sticky breadcrumb-less title |
| Modal title | 15 / 700 | Dialogs |
| Card title | 13.5 / 650 / −0.01em | Panel headers |
| Body / row primary | 13.5 / 500–600 | Buttons, file names, nav |
| Body secondary | 12.5–13 / 400–500 | Subtitles, descriptions |
| Meta | 11 / mono / `--t3` | Sizes, dates, speeds |
| Micro | 10.5 / mono 600 | Badges, tags, kbd, section labels (uppercase, +0.07–0.09em) |
| Log | 11.5 / mono / lh 1.75 | Task log console |

Baseline body: `14px / 1.55`, `-webkit-font-smoothing: antialiased`.

### 4.4 Geometry & elevation

| Token | Values |
|---|---|
| Radius | `--r-s` 8 px (controls) · `--r-m` 12 px (cards, dropzone) · `--r-l` 18 px (hero, modals, player chrome) · 99 px (pills, dots, avatars) |
| Shadow (dark) | `--shadow: 0 1px 2px rgba(2,4,10,.4), 0 12px 32px -16px rgba(2,4,10,.6)` · `--shadow-lg: 0 24px 64px -24px rgba(2,4,10,.8)` |
| Shadow (light) | Same geometry at ~25 % opacity |
| Border strategy | 1 px hairlines everywhere; borders brighten on hover (`--line` → `--line-2` → `--t3`) instead of shadows growing |
| Layout gap rhythm | 14 px between cards/stats · 9–10 px between sibling controls · 18 px card-body padding · 26 px view gutter |

### 4.5 Motion

| Class | Duration / easing | Applied to |
|---|---|---|
| Micro | 120–160 ms `ease` | Hovers, color/border transitions, button press (`translateY(1px)`) |
| Standard | 180–220 ms `ease` | Dropdown drawers, dropzone drag state, sidebar slide |
| Overlay in | 220 ms `cubic-bezier(.34,1.4,.5,1)` | Modal pop (scale .97 → 1 + translateY) |
| Overlay out | 200 ms `ease`, fade | All overlays |
| View enter | 300 ms `ease` `fadeUp` (6 px rise) | Every view switch |
| Toast in | 300 ms `cubic-bezier(.3,1.3,.5,1)` | Toast stack |
| Data | 500–800 ms `ease` / `cubic-bezier(.4,0,.2,1)` | Progress width, ring `stroke-dashoffset` |
| Ambient (decorative) | pulse 1.8 s · shimmer 1.6 s · EQ bars 1 s · hero drift 14 s · player aurora swirl 16 s / 23 s (counter-rotating) | Live indicators & cinema backdrop |

Rule of thumb: **nothing interactive waits longer than 220 ms**; ambient loops are GPU-cheap transforms/opacities only.

### 4.6 Z-index scale

| Layer | z-index |
|---|---|
| Topbar | 40 |
| Modal / palette overlay | 100 |
| Player | 120 |
| Sidebar scrim | 125 |
| Sidebar drawer (mobile) | 130 |
| Login | 140 |
| Toasts | 200 |

---

## 5. App Shell & Layout

```
┌──────────┬──────────────────────────────────────────────┐
│          │  topbar (sticky, glass, h 62)                │
│ sidebar  ├──────────────────────────────────────────────┤
│ 264 px   │  .view  (max-w 1460, pad 26/24, fadeUp)      │
│ sticky   │                                              │
│ full-h   │   [ stats grid — 4 col, gap 14 ]             │
│          │   [ two-col 1.9fr / 1fr, gap 14 ]            │
│ brand    │   [ quick actions — 4 col, gap 10 ]          │
│ nav ×5   │   [ card: recent files (mini-table) ]        │
│ library  │                                              │
│ server   │                                              │
│ user     │                                              │
└──────────┴──────────────────────────────────────────────┘
```

### 5.1 Sidebar (264 px, sticky, own scroll)

Ordered top→bottom:
1. **Brand** — 38 px gradient-rounded logo tile (cloud glyph), "PyCloud" 16.5/700, mono version pill `v8.6`.
2. **Primary nav** — Overview, Files, Media, Tasks (with live count badge), Settings. Active item: soft-gradient fill + 3 px gradient edge-bar bleeding to the sidebar's left edge + cyan icon.
3. **Library section** — sub-links for the three auto-categories, each with a fixed color dot (Manual = cyan, YouTube = pink, Old Root = slate `#94A3B8`) that reappear as chip dots and file-section dots. Color is the cross-view category signature.
4. **Server card** — pinned to bottom via `margin-top:auto`: pulsing ONLINE dot, endpoint `192.168.1.100:8080`, live uptime.
5. **User row** — gradient avatar, `admin / root session`, sign-out.

### 5.2 Topbar (sticky, h 62, glass)

`blur(14px)` over `--top-glass`, hairline bottom border. Left→right: hamburger (≤900 px only) → view title + mono subtitle → actions right-aligned: search pill (`Search files and actions` + `Ctrl K` kbd), theme toggle, **Upload** (soft), **New Download** (primary gradient, YouTube icon).

### 5.3 Content grids

| Grid | Definition |
|---|---|
| Stats | `repeat(4, 1fr)`, 14 px gap → 2-col @900 → 1-col @640 |
| Two-column | `1.9fr 1fr` (content-heavy + sidebar card) → 1-col @900 |
| Quick actions | `repeat(4, 1fr)` → 2-col @640 |
| Media grid | `auto-fill, minmax(215px, 1fr)` — fluid, no media queries needed |

---

## 6. Component Library

Full inventory as implemented in the prototype. All components are theme-token consumers; every interactive element has hover, active and `:focus-visible` states.

### 6.1 Buttons

| Variant | Spec |
|---|---|
| `.btn-primary` | 36 px, `--grad` fill, white 600 label, cyan glow shadow `0 4px 14px -6px rgba(34,211,238,.65)`, hover brightens + glow widens, `:active` sinks 1 px |
| `.btn-soft` | Card fill, `--line-2` border, hover border→`--t3` |
| `.btn-ghost` | Text-only, hover `--line` wash |
| `.btn-danger` / `.btn-danger-soft` | Solid `--bad` / tinted `--bad-bg` + red text |
| `.btn-sm` | 30 px / 12 px for row-level actions |
| `.icon-btn` | 34 px square grid-centered icon; `.bordered` adds card fill + border (theme toggle) |

### 6.2 Data display

- **Stat card** — icon well (30 px, tinted), label, mono 26 px value with unit, 52 px **SVG sparkline** (CPU cyan, RAM violet, network green) and a footer meta line. Disk uses a 104 px **conic progress ring** instead (gradient stroke, animated `stroke-dashoffset`) with used/free/total breakdown.
- **Donut + legend** — 150 px storage-by-category donut, center total, right legend rows (color chip 9 px, label, mono value, mono %).
- **Mini table** — recent files: type icon 30 px, name (ellipsis @300 px), mono size right, mono date right; row hover wash.
- **Badges** — mono uppercase 11 px pills: `b-done` green, `b-run` gradient-soft cyan (with pulsing dot), `b-queue` neutral, `b-cancel` red.
- **Chips** — 30 px filter pills with count `n` and optional category dot; active = gradient-soft fill + cyan border.
- **Tags / kbd** — mono micro-labels for tech facts (`Python 3.11`, `ffmpeg 6.1.1`) and keyboard hints.

### 6.3 Files

- **File row** — 36 px type-icon well → name (550) + inline rename input → mono meta (category label, date, duration) → hover-revealed action cluster (`play` / `rename` / `delete`, 29 px icon buttons) → right-aligned mono size + date columns (fixed widths 74/96 px keep true column alignment). Hover: card-fill + hairline. Rows are grouped under **category section headers** (dot + uppercase label + count).
- **Dropzone** — 1.5 px dashed, gradient icon well, "Drag & drop — or browse", helper text about the 400 MB rule; drag-over state = solid gradient-soft fill + cyan border; inline upload progress (name + mono % + 5 px shimmer bar).
- **Empty state** — dimmed icon, title, hint ("Nothing here yet").

### 6.4 Media

- **Continue-watching hero** — full-width 250 px min, layered gradient mesh with slow 14 s drift, center 62 px gradient play button, bottom-left title + mono meta, 62 %-watched progress line along the bottom.
- **Media card** — 16:9 gradient thumbnail (per-item hue), 42 px glass play overlay that fills with the brand gradient on hover, duration pill, name + mono meta; hover lifts 3 px + large shadow + 1.07× thumbnail zoom.
- **Audio card** — row layout with animated 4-bar **equalizer** (paused state freezes bars).

### 6.5 Tasks

Task row = icon well → name + mono source URL → status badge → progress bar (shimmer while running) + mono % → meta line (speed · ETA · size) → actions (logs / retry / cancel). Expanding **log console**: near-black terminal well, green mono text, dim `[queue]` prefixes, 210 px max-height scroll.

### 6.6 Overlays

- **Modal** — 480 px, radius 18, spring pop-in; header with tinted icon well (danger red for confirms), body, right-aligned footer.
- **Command palette** — 580 px, top-anchored at 12 vh; input row, grouped results (*Actions* with icon wells + *Files* with type icons + mono sub-labels), selected item = gradient-soft fill, footer with kbd legend.
- **Player** — full-viewport `#04060B`: title bar (CINEMA/AUDIO badge), stage with **counter-rotating aurora blobs** (blur 60 px) behind a glass poster tile, `.vtt` subtitle line, bottom controls — seek bar with buffer track + draggable knob, ±10 s, play/pause 46 px gradient button, CC toggle, volume, fullscreen. Pausing desaturates the aurora.
- **Login** — split screen 1.1fr/1fr; left = brand panel (mesh glows + 44 px grid overlay + gradient-clipped headline + mono feature pills), right = 360 px card. Left panel hidden ≤640 px.
- **Toasts** — bottom-right stack, 28 px type icon well, message, optional **action button (Undo)**, dismiss; spring in, fade out.

### 6.7 Utilities

Setting rows (title + description + trailing control, hairline-separated), danger card variant, quick-action tiles, switch, segmented control (Fetch format MP4/MP3), select with custom chevron, search inputs with leading icon, custom scrollbars (10 px, transparent track, content-box clipped thumb).

---

## 7. View Specifications

### 7.1 Overview (Dashboard)

- **Greeting header** — time-aware ("Good evening, admin"), status line: pulsing green dot "All systems operational" + tech tags.
- **4 stat cards** — CPU (sparkline), Memory (sparkline + used/total), Disk (ring + breakdown), Network (sparkline + up/down).
- **Two-column** — Background Tasks (top 2 live rows, "View all" link) | Storage donut by category.
- **Quick actions** — 4 tiles: Fetch from YouTube, Upload files, Open media center, Task console.
- **Recent files** — mini table, newest first.

### 7.2 Files

Header (item count + total size + sort select: Recent / Name / Largest) → toolbar (instant client-side filter input + category chips with live counts) → dropzone → **grouped sections** per category (dot + label + count), each with file rows. Behaviors: click-to-browse, drag & drop, inline rename (Enter saves, Esc reverts), file delete = instant + Undo toast, folder delete = confirm modal warning "removed immediately" (nuclear delete).

### 7.3 Media Center

Header ("7 videos · 1 audio track · smart subtitles (.vtt auto-load)") → continue-watching hero (resume at 62 %) → video grid (16:9 cards) → audio section (EQ rows). Click anywhere on card/hero opens the player.

### 7.4 Background Tasks

Header + "Clear completed" → full task list: running (pulsing badge + shimmer progress + live speed/ETA), queued ("waiting for the active task — sequential yt-dlp queue"), done, cancelled (retry). Logs expand inline per task.

### 7.5 Settings

Sections: Appearance (theme switch, matches topbar toggle), Server (endpoint, uptime, version tags), Behavior (auto-categorization, >400 MB download-mode), and a **Danger zone** card (red-tinted) — Stop PyCloud server with confirm modal → session ends at the login screen.

### 7.6 Fetch Video flow

Topbar **New Download** → modal: URL input (validates YouTube domains/paste-from-clipboard), segmented MP4/MP3, quality select, destination select → queues a task, toast confirms destination, queue promotes when the active task finishes.

---

## 8. Interaction & Motion Design

### 8.1 Keyboard map

| Keys | Action |
|---|---|
| `Ctrl / ⌘ + K` | Toggle command palette (works in every state) |
| `↑` `↓` | Move palette selection (auto-scrolls into view) |
| `Enter` | Execute selected palette item (action or file) |
| `Esc` | Close palette → any modal → player, in order |
| `Enter` / `Esc` | Commit / revert inline rename |

### 8.2 Live-data simulation (prototype)

| Loop | Cadence |
|---|---|
| CPU / RAM / network sparkline + value jitter | 1.4 s |
| Task progress + speed + ETA | 1 s |
| Uptime counter | 1 s |
| Boot greeting | Time-of-day aware; welcome toast after 1.2 s |

### 8.3 Micro-interaction rules

- File-row actions **fade in on row hover/focus-within** (never hidden from keyboard focus).
- Active nav = gradient edge bar + soft fill; hover = hairline wash only.
- Progress fills ≥ 0.5 s width transitions; running tasks add a shimmer sweep.
- All destructive hovers tint red (`--bad-bg` wash + red icon) — color previews the consequence.

---

## 9. Iconography & File-Type Color System

- **Style**: Lucide/Feather-derived stroke icons — 24 × 24 grid, `fill:none`, `stroke:currentColor`, `stroke-width:2`, round caps/joins; consumed via a single inline `<svg>` sprite (`<use href="#i-…">`), zero icon-font or CDN dependency.
- **Sizes**: 17 px nav · 15–16 px buttons/inputs · 14 px row actions · 11 px badges.

**File-type → color mapping (the product's core semantic system, used in rows, tables, palette and media grid):**

| Type | Extensions | Hue |
|---|---|---|
| Video | mp4, mkv, webm, mov | `--vio` violet |
| Audio | mp3, wav, flac, m4a | `--ok` green |
| Image | jpg, jpeg, png, gif, webp | `--pink` pink |
| Archive | zip, tar, gz, rar, 7z | `--amber` amber |
| Text / other | — | neutral `--t2` |
| Folder | — | brand gradient cyan |

**Category dot colors** (cross-view signature): Manual Uploads = cyan · YouTube Library = pink · Old Root Files = slate `#94A3B8`.

---

## 10. Accessibility

Measured contrast (WCAG 2.1, computed from the actual token values):

| Pair | Ratio | Verdict |
|---|---|---|
| OLD dim meta `#5f5f5f` on `#161616` | 2.83:1 | **Fail** (replaced) |
| OLD muted `#8a8a8a` on `#1a1a1a` | 5.04:1 | Pass but cramped |
| NEW `--t1` on `--card` (dark) | 16.40:1 | AAA |
| NEW `--t2` on `--card` (dark) | 7.09:1 | AA |
| NEW `--cyan` / `--ok` / `--bad` / `--vio` / `--pink` on card (dark) | 6.7–10.7:1 | AA |
| NEW `--t1` / `--t2` on white (light) | 17.4 / 6.7:1 | AA/AAA |
| Player title on stage | 20.3:1 | AAA |

Rules and honest caveats:

1. **`--t3` is metadata-only** (timestamps, counts, hints) — 3.5:1 dark / 2.7:1 light. It must never carry information required to complete a task; essential text uses `--t2` minimum. This rule is enforced in review.
2. **Gradient-button trade-off**: white label on `--grad` measures 1.8–3.7:1 across the stops. Accepted for brand consistency (Linear/Vercel precedent, 600-weight labels, generous size); if strict AA is mandated, ship variant `--grad-deep: linear-gradient(135deg,#0891B2,#2563EB)` for text-bearing buttons.
3. **Focus**: global `:focus-visible` = 2 px `--cyan` outline, 2 px offset — never removed.
4. **Full keyboard path**: nav, palette, modals, player, rename, upload — all reachable without a mouse.
5. **Production TODOs**: `prefers-reduced-motion` (disable ambient loops, keep functional transitions ≤150 ms); `aria-live="polite"` on the toast stack; `role="dialog"` + focus trap on modals; `aria-label` on all icon-only buttons.

---

## 11. Responsive Strategy

| Breakpoint | Layout changes |
|---|---|
| **> 1180 px** (base) | 264 px sidebar + content, max-w 1460 |
| **≤ 1180 px** | Sidebar collapses to **70 px icon rail** (labels, library, server card, user text hidden; icons centered). Stats stay 4-col |
| **≤ 900 px** | Sidebar becomes **off-canvas drawer** (264 px, scrim, hamburger appears); stats → 2-col; two-col/grid-2 → 1-col; topbar subtitle hidden; search pill flexes |
| **≤ 640 px** | Topbar title + file dates hidden; search pill collapses to 38 px icon (opens palette); stats → 1-col; quick actions → 2-col; row rename action hidden (rename via palette/menu); file actions always visible (no hover on touch); buttons label-hidden; login brand panel hidden |

Media grid needs no breakpoint — `auto-fill / minmax(215px, 1fr)` self-adjusts.

---

## 12. Architecture & Technology

### 12.1 Constraints (from the product's DNA)

- One Python script serves everything; no Node, no bundler, no package.json in the user's deployment path.
- Must run offline / LAN-only — no hard runtime dependencies on CDNs.
- Basic-auth, single-user (admin) — no multi-tenant complexity.

### 12.2 Prototype (current deliverable)

`pycloud-redesign.html` — one self-contained file (~2,000 lines):

- Inline `<style>` (token-driven CSS, no preprocessor) + inline `<script>` (vanilla ES6, no libraries).
- Inline SVG sprite for all icons.
- Google Fonts `<link>` for Inter/JetBrains Mono with full system-font fallback (already renders correctly offline).
- All data is client-side simulated (see §8.2) — deliberately, so the design could be approved before any backend wiring.

### 12.3 Production integration plan

Keep the zero-build philosophy:

```
secure_server.py
└── serves /static
    ├── app.html        (shell — from prototype, minus simulated data)
    ├── styles.css      (tokens + components, extracted verbatim)
    ├── app.js          (render/state modules, fetch-driven)
    └── icons.svg       (sprite)
```

Wiring contract — each simulated function becomes a `fetch`:

| UI function (prototype) | Backend source |
|---|---|
| `tickStats()` sparklines/ring | `/api/stats` polled every 1.4 s (psutil-equivalent values the server already collects) |
| `renderFiles()` / donut | `/api/files` (name, size, mtime, category, type) |
| `addFiles()` dropzone progress | `POST /api/upload` with XHR progress events |
| rename / delete actions | `POST /api/rename` · `POST /api/delete` (folders return a `nuclear:true` flag → confirm modal) |
| task list, logs, cancel/retry | `/api/tasks` + log streaming (poll or chunked) from the yt-dlp queue |
| `openPlayer()` | existing stream endpoints; `.vtt` fetched alongside for the subtitle line |
| login card | existing basic-auth handshake |
| theme | `localStorage('pc-theme')` — client-side only |

**Future options** (only if the project outgrows vanilla): Preact + Vite island architecture, or htmx for server-rendered partials. **Recommendation: stay vanilla** — the entire interactive surface here (list rendering, palette fuzzy-filter, progress loops) is comfortably within hand-written JS, and "one script, no frameworks" is a stated product value.

---

## 13. Implementation Roadmap

| Phase | Scope | Status |
|---|---|---|
| **0. Concept prototype** | Full design system + all views + simulated data, single file | ✅ Done — `pycloud-redesign.html` |
| **1. User confirmation** | Review of this spec + prototype; token/layout adjustments | ◀ Current |
| **2. Asset extraction** | Split into `styles.css` / `app.js` / `icons.svg`; add a11y TODOs (§10.5) | Planned |
| **3. Backend wiring** | Replace simulators with the §12.3 fetch contract; error/empty/offline states | Planned |
| **4. Hardening** | Reduced-motion, aria roles, LAN font fallback (self-host WOFF2), README screenshot refresh | Planned |

---

## 14. Before → After Summary

| Audit finding | Fix in redesign |
|---|---|
| S1 no navigation | App shell: 5-view sidebar + sticky topbar + command palette |
| S2 no grid/max-width | 1460 px container, 8-pt gap rhythm, fixed meta columns |
| S3 FAB occlusion | FAB removed; actions live in topbar + quick tiles + row hovers |
| S5/S6 flat lists, native file input | Type-icon rows grouped by category; styled dropzone with drag state + named progress |
| V1 emoji icons | Lucide-style stroke sprite, currentColor |
| V2 six random accents | One brand gradient + semantic hue system (§4.1, §9) |
| V3 conflicting buttons | Single 6-variant button family (§6.1) |
| V4 contrast fail 2.83:1 | Token ramp AA+ for all essential text (§10) |
| V5 status = data colors | Separate status badge vs. file-type hue namespaces |
| V6 inconsistent units | All machine data mono with explicit units |
| V7 competing scales | 11-step type scale (§4.3) |
| V8 static stats | Sparklines, ring, donut at 1.4 s cadence |
| U1 no delete confirm | Modal for nuclear folder delete; Danger zone in Settings |
| U2 no undo | Undo action button on delete toasts |
| U3 mouse-only | Full keyboard map + `Ctrl K` palette |
| U4 bare progress | Filename + % + shimmer + cancel |
| U5 hidden logs | Inline expandable terminal console per task |

---

## 15. References

- Linear — https://linear.app (app shell, quiet chrome, keyboard-first)
- Vercel Geist design system — https://vercel.com/geist (dark-first tokens, Inter/JetBrains Mono)
- Raycast — https://raycast.com (command palette, blurred overlays, gradient glows)
- Lucide icons — https://lucide.dev (stroke icon language)
- Tailwind CSS color palette — https://tailwindcss.com/docs/customizing-colors (hue steps 400/600 reused)

*This document is the single source of truth for the PyCloud redesign. Any visual change starts here: update the token tables, then the code.*
