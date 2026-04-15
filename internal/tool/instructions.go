// Package tool — server-level MCP instructions.
//
// DefaultInstructions is advertised to MCP clients in the initialize
// response (via mcpserver.WithInstructions). Claude Code, Claude
// Desktop, and most other MCP clients surface this text to the agent's
// system prompt, which is the single most reliable lever we have for
// telling an agent when to reach for pagefault's pf_* tools instead of
// its built-ins. Operators can override this with
// `server.mcp.instructions` in the YAML config — the default below is
// the fallback when that field is empty.
package tool

// DefaultInstructions is the server-level instruction string emitted in
// the MCP initialize response when no operator override is configured.
// Keep it prescriptive: tell the agent both *when* to call pf_* tools
// and *when not to*, since the failure mode of a vague description is
// an eager agent that spams pf_scan for every question.
//
// The content is organised as: intro + core rule → when to reach →
// which tool → multi-agent routing → practical guidance. Signal
// phrases appear in both English and Chinese because real-deployment
// traces showed cold agents missing cross-language equivalents of the
// same question; the list is not exhaustive, it just teaches the
// shape of the queries that should route here.
const DefaultInstructions = `pagefault is the user's personal memory server. The pf_* tools
read and write a private knowledge store configured by the
operator — daily notes, journals, project documents, past
decisions, and (critically) **a searchable archive of past
conversations between the user and AI assistants**. Pagefault
backends commonly include lossless chat-history compression
(e.g. lossless-lcm), per-session transcripts, or embedding
indices over past sessions, which means the answer to "what did
we talk about before" / "what did I tell you last week" / "我最近
跟你聊过[X]吗" almost always lives here, NOT in your current
conversation window.

## Core rule: do not claim "I don't remember" without checking

If the user asks about their own past activity, past decisions,
or a past conversation, and the answer is not already in your
current conversation context, you MUST call a pf_* tool before
answering. Specifically:

- **Do not say "I don't remember" / "我不记得" / "I have no
  record of that" / "we didn't discuss that" without having
  first tried pf_scan and/or pf_fault.** Your conversation
  window is only this session; the user's pagefault archive
  covers everything else. A wrong "no memory" answer is worse
  than a slow one, because it teaches the user pagefault is
  broken when really you just skipped the tool call.
- Prefer a fast pf_scan round-trip for concrete keywords; fall
  back to pf_fault for natural-language / fuzzy recall. It is
  fine to try both, in that order, before replying.
- Only after both miss should you tell the user you couldn't
  find it — and even then, say explicitly "I searched pagefault
  and found nothing matching X", not "I don't remember".

## When to reach for these tools

Call a pf_* tool when the user's request implies a lookup into
their personal knowledge or past activity. This includes both
notes/documents the user has written AND past conversations
stored in the archive.

**Signal phrases (English):**

- "What did I note about X", "where did I write about X",
  "pull up my notes on X", "remind me what I decided about X".
- "What did we talk about [last week / yesterday / on date]",
  "what did I tell you about X", "did I mention X before",
  "you told me about X — find it again".
- "What was I doing [in March / last Tuesday / recently]",
  "summarise my activity over [timeframe]".
- The user asks you to remember, save, log, or journal
  something — or a reasoning step produces a durable insight
  worth persisting.

**Signal phrases (Chinese — same shapes, common phrasings):**

- "我[时间]在干嘛" / "我[时间]做了什么" / "我[时间]干了啥"
  (e.g. "我三月在干嘛", "我4月2号做了些什么", "我昨天干啥了").
- "我最近和你聊了什么[X]" / "我跟你说过[X]吗" / "你之前跟我说过
  [X]" / "我之前提过[X]吗".
- "我的笔记里有[X]吗" / "我之前是怎么决定[X]的" / "帮我翻一下
  [X]的记录" / "搜一下[X]".
- "记一下" / "帮我存一下" / "记到我的笔记里" / "加到日记里" (write,
  not read — route to pf_poke).

**Temporal references matter.** Any user question that combines
a past-time marker ("last week", "recently", "on Monday",
"三月", "4月2号", "最近", "上周") with a first-person verb
("I did", "we discussed", "我做了", "我们聊了") is almost
certainly a pagefault question, not a conversation-context
question. Default to searching pagefault.

**Do NOT call these tools for:**

- General world knowledge ("what is the capital of France",
  "how does TCP work"). Use your own training.
- Questions about the current repository / open files. Use the
  built-in file tools.
- Questions answerable from the current conversation window
  alone, without any reference to past activity.
- Toxicology, medical, legal, or other topical reference
  lookups that sound like "search my memory" but are really
  world-knowledge questions. If the user asks "is oleander
  toxic", that is a world-knowledge question — pagefault only
  helps if the user is asking "what did I *write* about
  oleander" or "did I note my oleander decision anywhere".

pagefault is for recall of the user's own notes, decisions, and
past conversations — not a general search engine.

## Which tool to pick

- pf_maps   — start here when you do not yet know what memory regions
              exist. Lists every pre-composed bundle (daily notes,
              projects, etc.) with a name and description. Cheap.
- pf_scan   — keyword / substring search across configured backends.
              The right default when the user's query contains
              distinctive concrete tokens (names, dates, filenames).
              Returns ranked snippets. Note: pf_scan is a grep, not
              a semantic search — a full natural-language question
              like "what did I do on April 6" may return nothing
              because no file literally contains that phrase. If
              pf_scan returns empty on a sentence-shaped query, go
              to pf_fault instead of giving up.
- pf_peek   — read one specific resource by URI when you already know
              where it lives (e.g. memory://memory/2026-04-11.md).
              Usually follows a pf_scan hit you want to read in full,
              or a direct user reference.
- pf_load   — fetch an entire named region assembled from its
              configured sources. Use when the user asks to pull up a
              whole bundle ("load my daily notes for this week").
- pf_fault  — heaviest tool: spawn a subagent that reasons over the
              memory store and returns a synthesised answer. Reach
              for it when the question needs intelligent retrieval
              (cross-reference, summarisation, fuzzy semantic
              lookup) or when pf_scan misses on a natural-language
              query. A real pf_fault call typically takes 20-40
              seconds before it starts replying and can run past a
              minute — do not set timeout_seconds below 120.
- pf_ps     — list the subagents pf_fault and pf_poke mode:"agent"
              can spawn, with a description for each one. **Call
              this before pf_fault (or pf_poke mode:agent) whenever
              more than one subagent is configured** — the
              descriptions are how you route the user's query to
              the right agent (work vs personal, short-term vs
              long-term, journal vs project notes). Cheap and local;
              no subagent spawn.
- pf_poke   — write content back into memory. mode:"direct" appends
              to a specific URI (filesystem backends only);
              mode:"agent" delegates placement to a subagent that
              decides where the content belongs. Use when the user
              says remember / save / log / 记一下, or when you have
              produced a durable insight the user will want back
              later.

## Multi-agent routing

When pf_ps reports more than one agent, **do not rely on the
"first configured agent" fallback** — call pf_ps, read each
agent's description, and pick the one whose scope matches the
user's question. Common patterns: a "work" agent (meetings,
tickets, infra) next to a "personal" agent (journal, health,
travel); a "short-term" agent (today's scratch notes) next to a
"long-term" agent (curated knowledge). If the user's question
straddles scopes (e.g. "what did I do yesterday" — which mixes
work and personal), make two pf_fault calls and merge the
results. A single-agent config can skip pf_ps and go straight
to pf_fault; the fallback exists for that case.

## Practical guidance

- Prefer pf_scan → pf_peek for most concrete-keyword questions;
  it is cheap and usually sufficient.
- For natural-language / sentence-shaped questions, skip pf_scan
  and go directly to pf_fault — pf_scan is a grep and will
  usually return nothing for those.
- Call pf_maps once at the start of a session if you have never
  seen this pagefault instance before, so you know what regions
  exist.
- Default pf_fault.timeout_seconds is 120 and you almost always
  want to raise it, not lower it. A 30 or 60 second timeout
  will truncate most real runs; budget 180-300 seconds for deep
  cross-source lookups.
- When writing with pf_poke in direct mode, pick a URI that
  matches the configured write_paths allowlist; the server will
  reject writes outside it.`
