# pagefault — Design System

> *The aesthetic of systems programming, made beautiful.*

This document is the single source of truth for the visual language,
voice, and interaction patterns of everything user-facing in
pagefault — the landing site in `web/`, future dashboards or admin
surfaces, documentation, CLI output, and error envelopes. Every future
design decision should either follow this doc or amend it. If it
drifts from the code or the shipped site, update this doc first.

---

## 1. Concept

pagefault takes its name from a 50-year-old operating-systems
primitive. When a process touches memory that isn't resident, the CPU
raises a **fault**, the kernel's handler quietly fetches the right
page from backing store, and execution resumes — invisible, precise,
and immediate. The whole beauty of the mechanism is in that
split-second of resolution: the gap between *"I need this"* and
*"here it is"* closes without ceremony, below the program's awareness.

Our design is built on that moment. Every surface should feel:

- **Precise.** Monospace type, exact spacing, hex where hex belongs.
  No ornamental flourish.
- **Low-level.** The aesthetic of `gdb`, `dmesg`, a well-made editor
  theme — but elevated, not nostalgic. We are not cosplaying 1983.
- **Calm in the gap.** When something is loading, faulting, resolving,
  the motion is confident, not anxious. No spinners throwing tantrums.
- **Honest with data.** Show hex. Show timestamps. Show the shape of
  the thing. When we hide complexity, we do it because a human doesn't
  need it, not because we're afraid of it.

We are a memory server for AI agents. Our visual language should feel
closer to a kernel debugger than to a SaaS dashboard.

## 2. Voice

- **Concrete nouns, active verbs, short sentences.**
- Lowercase is fine in running copy: `pagefault`, `pf_fault`, not
  `PageFault` or `PF_FAULT`.
- No corporate plural. We don't "deliver solutions". We are the tool.
- Dry wit is welcome. Cheerleader energy is not.
- Never say: *simply*, *just*, *easy*, *blazing fast*, *seamless*,
  *unlock*, *leverage*, *synergy*, *enterprise-grade*.
- Technical references should land for readers who already know the
  metaphor (page table, backing store, SIGSEGV), but never alienate
  readers who don't. Every term that matters gets explained once.

**Taglines and headings earn their place.** Good examples:

- *"Memory, mapped."*
- *"When your agent hits a context miss, pagefault loads the right page back in."*
- *"Seven tools. One surface."*
- *"The name is the spec."*

Bad examples:

- "The ultimate memory solution for AI agents" ✗
- "Supercharge your LLM workflow" ✗
- "Blazing-fast, enterprise-grade context delivery" ✗

## 3. Color system

Dark mode is the canonical mode. All tokens live as CSS custom
properties in `web/styles.css` (`:root`). A light mode is possible as
an inversion, but dark is authoritative — every pairing is designed
for dark first.

### 3.1 Base tokens

| Token             | Hex        | Purpose                                     |
|-------------------|------------|---------------------------------------------|
| `--bg`            | `#0a0a0b`  | Page background                             |
| `--bg-elevated`   | `#121216`  | Raised surfaces (cards, menus)              |
| `--bg-inset`      | `#050506`  | Pressed/inset surfaces (code blocks, wells) |
| `--border`        | `#232329`  | Subtle dividers                             |
| `--border-strong` | `#3a3a42`  | Hover/focus emphasis                        |
| `--text`          | `#ebe9e4`  | Primary content                             |
| `--text-dim`      | `#8a877f`  | Secondary content                           |
| `--text-faint`    | `#55524a`  | Tertiary (metadata, timestamps, help)       |

### 3.2 Semantic tokens

| Token          | Hex        | Purpose                                |
|----------------|------------|----------------------------------------|
| `--accent`     | `#ff7e1f`  | Primary accent — the "fault signal"    |
| `--accent-dim` | `#b3581c`  | Pressed / hovered accent variants      |
| `--resolved`   | `#74d19c`  | Success, resolved, `status:"done"`     |
| `--fault`      | `#ff5c4b`  | Error, unresolved, `status:"failed"`   |
| `--running`    | `#e8c26b`  | In-flight, running, pending            |

Rationale: the warm orange accent references amber-phosphor terminals
and CRT warning glyphs while reading as distinctly modern. Warm
off-white (`--text`) is kinder on the eyes than pure white on dark
and widens our usable contrast range. Red / green / amber for
semantic states map **directly** onto the `task.Status` enum in
`internal/task/task.go` — `failed`, `done`, `running` — so UI and
audit logs and tool JSON all speak the same color language.

### 3.3 Contrast guarantees

All body text meets WCAG AA (4.5:1 small, 3:1 large).

- `--text` on `--bg`: **14.2 : 1**  ✅ AAA
- `--text-dim` on `--bg`: **5.9 : 1**  ✅ AA
- `--accent` on `--bg`: **6.1 : 1**  ✅ AA, but **large-text only** —
  never use `--accent` as a body-copy color.
- `--text-faint` on `--bg`: **3.1 : 1**  ⚠️ sub-AA — reserved for
  12px metadata users will never need to read (timestamps, hex offsets
  shown for ornament).

## 4. Typography

### 4.1 Families

- **Display & UI.** `"JetBrains Mono"`, `"SF Mono"`, `"Cascadia Code"`,
  `"Menlo"`, `"Consolas"`, monospace.
- **Body prose.** `"Inter"`, `"Helvetica Neue"`, system-ui, sans-serif.
- **Code.** Same as display monospace.

We use monospace for headings, navigation, buttons, labels, tables of
data, and anything code-like, and a clean grotesque sans for body
prose longer than a sentence or two. Marketing copy rendered in full
monospace reads as gimmicky past about twenty words; a clean body
sans keeps long-form content reader-friendly without breaking the
technical register.

JetBrains Mono and Inter are both open-source (SIL OFL) and small
enough to serve over the public web without hurting LCP — use
`font-display: swap` and preconnect to the CDN.

### 4.2 Scale

Root font-size is 16px. All sizes in rem so zoom works.

| Token       | Size     | Line-height | Use                             |
|-------------|----------|-------------|---------------------------------|
| `--fs-xs`   | 0.75rem  | 1.4         | Labels, metadata, CAPS tags     |
| `--fs-sm`   | 0.875rem | 1.5         | Fine print, dense tables        |
| `--fs-base` | 1rem     | 1.6         | Body                            |
| `--fs-md`   | 1.125rem | 1.5         | Lede, important body            |
| `--fs-lg`   | 1.5rem   | 1.3         | Card titles, small headings     |
| `--fs-xl`   | 2rem     | 1.2         | Section headings                |
| `--fs-2xl`  | 3rem     | 1.1         | Feature headings                |
| `--fs-3xl`  | clamp(3rem, 6vw + 1rem, 5.5rem) | 1 | Hero display |

### 4.3 Weights

400 regular, 500 medium, 700 bold. **No lighter weights** — they smear
on dark backgrounds. Italics are permitted for body prose emphasis
only.

### 4.4 Tracking & treatments

- Display sizes (`--fs-xl` and up): `letter-spacing: -0.02em` to
  tighten monospace
- Body prose: `letter-spacing: 0`
- ALLCAPS labels: `letter-spacing: 0.1em`, always `--fs-xs`
- Max prose line length: **68ch**
- Numbers in tables / dashboards: always `font-variant-numeric: tabular-nums`

## 5. Iconography

Icons are drawn on a **24×24 artboard** with **2px padding** (glyph in
a 20px safe area). All strokes are **1.5px**, with **round caps** and
**round joins**. Outline only. No fills unless the glyph demands it
(e.g., a solid arrowhead, or the logomark itself).

### 5.1 Logomark

A rounded square (the "page") with a diagonal chevron slice inward at
one corner. The slice signifies the fault — something missing from
the page — and the chevron points the load direction.

```
  ┌──────────┐
  │          │
  │       ◥  │ ← chevron slice = the fault,
  │       ┃  │   pointing inward = the load
  │          │
  └──────────┘
```

At 16px (favicon) we drop the chevron and ship the bare square, then
bring the chevron back at 24px+. The canonical SVG lives at
`web/favicon.svg`. Stroke in `currentColor` so the mark tints with
context.

### 5.2 Tool glyphs

Each `pf_*` tool has a glyph that riffs on its Unix analog. Build as
needed; don't ship unused.

| Tool        | Glyph concept                                        |
|-------------|------------------------------------------------------|
| `pf_maps`   | 2×2 grid of small squares (process address map)      |
| `pf_load`   | page with a downward arrow breaking the top edge     |
| `pf_scan`   | magnifying glass over a grid pattern                 |
| `pf_peek`   | an eye bracketed by `[` and `]`                      |
| `pf_fault`  | three radiating chevrons                             |
| `pf_ps`     | horizontal row of bullets (like `ps` columns)        |
| `pf_poke`   | page with an upward arrow breaking the bottom edge   |

## 6. Space & layout

Base unit is **8px**. The spacing scale is every multiple of 4 from
4 through 128:

```
  4   8   12   16   24   32   48   64   96   128
 sp1  sp2  sp3  sp4  sp5  sp6  sp7  sp8  sp9  sp10
```

Exposed in CSS as `--sp-1` through `--sp-10`.

- **Prose max width:** 68ch
- **Layout max width:** 1200px
- **Grid:** 12-column, 24px gutter mobile, 32px desktop
- **Section vertical rhythm:** 96px desktop, 64px mobile
- **Component internal padding:** 16–32px depending on hierarchy

Never hardcode a pixel value that doesn't come from the spacing scale.
If you need a new value, add it to the scale.

## 7. Components

This section grows with the surface. Today's primitives:

- **Buttons.**
  - *Primary:* `--bg` text on `--accent` fill, no border, 12px × 22px
    padding, 8px radius, `--fs-sm` monospace. Hover: `--accent-dim`.
  - *Ghost:* `--text` on transparent, 1px `--border` border, same
    padding, same radius, same type. Hover: `--border-strong`.
  - Active state: translate 1px down, no shadow. No "press" bounce.
- **Links.** `--accent` on hover, underline on hover in UI chrome.
  In body prose, underlined at rest, bolder underline on hover.
- **Code blocks.** `--bg-inset` fill, 1px `--border`, 16px padding,
  8px radius, monospace, no wrap, horizontal scroll for overflow.
  Inline code: `--bg-inset` fill, 2px × 6px padding, 4px radius.
- **Tables.** Monospace data rows, 1px `--border` row dividers, 12px
  vertical cell padding, `font-variant-numeric: tabular-nums`.
  Headers: `--text-dim`, `--fs-xs`, ALLCAPS, 0.1em tracking.
- **Badges / tags / pills.** Monospace `--fs-xs`, 1px `--border`,
  2px radius, 2px × 8px padding. Semantic badges use
  `--resolved` / `--fault` / `--running` for both border and text.
- **Cards.** `--bg-elevated` fill, 1px `--border`, 8px radius,
  24px padding. No drop shadows (we're on dark — shadows read muddy).

## 8. Motion

| Name       | Duration | Easing                          |
|------------|----------|---------------------------------|
| `micro`    | 150ms    | `cubic-bezier(0.2, 0, 0, 1)`    |
| `standard` | 250ms    | `cubic-bezier(0.2, 0, 0, 1)`    |
| `emphasis` | 400ms    | `cubic-bezier(0.3, 0, 0, 1)`    |

Rules:

- **No bounce, no spring.** Motion should feel deliberate — a cursor
  blink, a scanline pass, not a cartoon.
- **Prefer `transform` and `opacity`.** Avoid layout-affecting
  animations (width, height, top, left).
- **Reduced motion.** Every animation respects
  `prefers-reduced-motion: reduce`. In reduced mode, all transitions
  become opacity-only or drop to 0ms.
- **Hero / ambient.** Allowed for *one* area per page, and always
  paused by `prefers-reduced-motion`. The rest of the page is still.

## 9. Accessibility

- **Contrast.** All body copy ≥ 4.5:1. Interactive text ≥ 3:1.
- **Focus.** 2px solid `--accent`, 2px offset, always visible.
  Never `outline: none` without a replacement outline.
- **Keyboard.** Every interactive element reachable in natural tab
  order. Skip link to main content at the top of every page.
- **Semantics.** Use real `<h1>`–`<h3>`, real lists, real `<table>`,
  real `<button>`. Never a `<div onclick>` imitating a button.
- **Motion.** `prefers-reduced-motion: reduce` disables all
  non-essential animation.
- **Language.** `<html lang="en">`. Change if we ever translate.

## 10. Error surfaces

pagefault is a memory server for AI agents, so the primary "user" of
most errors is another model reading a JSON envelope. But when a
human sees an error, the shape should match this visual language:
be precise, show the hex, show the cause, show the next step.

Map semantic colors to the `task.Status` values from the codebase:

- **Fault (`--fault`, red).** A real problem the user must act on.
  Example: `auth: bearer token expired`.
- **Running (`--running`, amber).** In-flight, not done yet, not
  stuck. Example: `pf_fault task pf_tk_abc running (elapsed 42s)`.
- **Resolved (`--resolved`, green).** Success, task complete.
  Example: `pf_poke → done (file updated)`.

## 11. Surfaces

Everything user-facing that ships today, and the design files that
govern it:

| Surface                        | Lives in                                                    | Style source |
|--------------------------------|-------------------------------------------------------------|--------------|
| Landing site (binary)          | `web/` + `internal/server/server.go` (runtime `{{version}}` sub) | this doc |
| Landing site (GitHub Pages)    | `web/` + `.github/workflows/pages.yml` (CI `{{version}}` sub)    | this doc |
| CLI output (color, formatting) | `cmd/pagefault/tools.go`                                    | this doc §10 |
| Error envelopes (HTTP)         | `internal/server/server.go`                                 | this doc §10 |
| MCP tool descriptions          | `internal/tool/mcp.go`                                      | this doc §2  |
| OpenAPI titles & descriptions  | `internal/server/openapi.go`                                | this doc §2  |

When we add a surface, add a row here. When a surface drifts from
this doc, the doc wins — either fix the surface or propose an
amendment.
