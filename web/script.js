/* ──────────────────────────────────────────────────────────
 * pagefault · hero terminal animation
 *
 * Cycles through a canonical pf_fault call — prompt, fault,
 * handler, resolved, results — to make the page-fault metaphor
 * legible in ~8 seconds. Respects prefers-reduced-motion by
 * rendering the final frame statically.
 *
 * Governed by docs/design.md §8 (motion) and §3.2 (semantic colors).
 * ────────────────────────────────────────────────────────── */

(() => {
  "use strict";

  const body = document.getElementById("terminalBody");
  if (!body) return;

  const prefersReduced = window.matchMedia(
    "(prefers-reduced-motion: reduce)"
  ).matches;

  // ── frames ─────────────────────────────────────────────
  // Each frame is an array of lines. Lines are appended
  // sequentially with a small delay; between frames we pause,
  // then reset. A cycle is ~8s in standard motion.

  const frames = [
    // 1. incoming request from agent
    [
      {
        cls: "",
        html: '<span class="tl-prompt">$</span> pagefault fault <span class="tl-dim">"find my notes on virtual memory"</span>',
      },
      {
        cls: "tl-dim",
        html: "→ parsing request · agent = <span class=\"mono\">sonnet</span> · timeout = 120s",
      },
    ],
    // 2. fault raised
    [
      {
        cls: "tl-fault",
        html: '<strong>⚠ fault</strong>  context miss — "virtual memory notes" not resident',
      },
      {
        cls: "tl-dim",
        html: "  addr = <span class=\"mono\">memory://notes/os/*</span>  ·  backend = subagent-cli",
      },
    ],
    // 3. handler spawning — running state
    [
      {
        cls: "tl-running",
        html: '<strong>» handler</strong>  spawning subagent <span class="mono">pf_sp_9c4d3a11…</span>',
      },
      {
        cls: "tl-running tl-caret",
        html: "  task = <span class=\"mono\">pf_tk_7a3f22…</span>  ·  status = running  ·  elapsed 1.2s",
      },
    ],
    // 4. resolution — loaded pages
    [
      {
        cls: "tl-resolved",
        html: "<strong>✓ resolved</strong>  status = done  ·  2 pages  ·  847 tokens  ·  2.3s",
      },
      {
        cls: "tl-dim",
        html: "  loaded <span class=\"mono\">memory://notes/os/paging.md</span>",
      },
      {
        cls: "tl-dim",
        html: "  loaded <span class=\"mono\">memory://notes/os/tlb.md</span>",
      },
      {
        cls: "",
        html: '<span class="tl-prompt">$</span> <span class="tl-dim">// agent resumes with the right page in context</span>',
      },
    ],
  ];

  // ── reduced motion: render end state once, stop. ───────

  if (prefersReduced) {
    renderAll();
    return;
  }

  // ── cycle loop ─────────────────────────────────────────
  //
  // Cancellation uses a token object: every call to start()
  // creates a new token and marks the previous one cancelled.
  // The in-flight loop checks its *own* token at every await
  // boundary, so at most one loop mutates the terminal body at
  // a time. Off-screen pausing is driven by IntersectionObserver
  // only — setTimeout is throttled on hidden tabs already, so a
  // separate visibilitychange handler would just duplicate state.

  const LINE_GAP_MS   = 260;  // delay between lines within a frame
  const FRAME_HOLD_MS = 900;  // hold after a frame completes
  const FINAL_HOLD_MS = 3200; // hold on the last frame before reset
  const RESET_FADE_MS = 280;

  let token = { cancelled: true };

  async function cycle(myToken) {
    while (!myToken.cancelled) {
      body.innerHTML = "";
      for (let i = 0; i < frames.length; i++) {
        if (myToken.cancelled) return;
        const frame = frames[i];
        for (const line of frame) {
          if (myToken.cancelled) return;
          appendLine(line);
          await sleep(LINE_GAP_MS);
        }
        if (myToken.cancelled) return;
        await sleep(i === frames.length - 1 ? FINAL_HOLD_MS : FRAME_HOLD_MS);
      }
      if (myToken.cancelled) return;
      await fadeOut();
    }
  }

  function start() {
    if (!token.cancelled) return; // already running
    token = { cancelled: false };
    cycle(token);
  }

  function stop() {
    token.cancelled = true;
  }

  function appendLine({ cls, html }) {
    const el = document.createElement("div");
    el.className = "terminal-line" + (cls ? " " + cls : "");
    el.innerHTML = html;
    body.appendChild(el);
  }

  function renderAll() {
    body.innerHTML = "";
    for (const frame of frames) {
      for (const line of frame) appendLine(line);
    }
  }

  async function fadeOut() {
    body.style.transition =
      `opacity ${RESET_FADE_MS}ms var(--ease-standard, ease)`;
    body.style.opacity = "0";
    await sleep(RESET_FADE_MS);
    body.innerHTML = "";
    body.style.opacity = "1";
    await sleep(120);
  }

  function sleep(ms) {
    return new Promise((r) => setTimeout(r, ms));
  }

  // IntersectionObserver is the single source of truth for
  // running vs. paused. Pause when the terminal scrolls off
  // screen; resume the moment it scrolls back.
  const io = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (e.isIntersecting) start();
        else stop();
      }
    },
    { threshold: 0.15 }
  );
  io.observe(body);
})();
