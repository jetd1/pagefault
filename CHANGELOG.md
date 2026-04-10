# Changelog

All notable changes to pagefault are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Changed
- Repo and Go module renamed from `page-fault` to `pagefault` for consistency with the binary, CLI, and product name. Go module path is now `github.com/jet/pagefault`. Two-word "page fault" references to the OS concept in explanatory prose are preserved intentionally.

## 0.1.0 (2026-04-10)

### Added
- Initial project scaffold and Phase 1 MVP
- Filesystem backend with glob include/exclude, sandbox, auto-tag, URI scheme mapping
- Config package: YAML loader with ${ENV} substitution and validation
- Auth package: BearerTokenAuth (JSONL tokens file) and NoneAuth
- Filter package: PathFilter (allow/deny globs) and TagFilter
- Audit package: JSONL audit logger
- Tool dispatcher with pre/post filter pipeline
- Phase-1 tools: `list_contexts`, `get_context`, `search`, `read`
- HTTP server: chi router with MCP (`/mcp`) and REST (`/api/{tool}`) transports
- CLI: `pagefault serve`, `pagefault token create/ls/revoke`, `pagefault --version`
- Minimal config example and demo data
- Initial docs: api-doc.md, config-doc.md, architecture.md, CLAUDE.md
