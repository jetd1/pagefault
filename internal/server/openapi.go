// Package server — OpenAPI spec generation for the REST transport.
//
// The /api/openapi.json endpoint emits a machine-readable description of
// every enabled pf_* tool. ChatGPT Custom GPT Actions (and any other
// OpenAPI-consuming client) can import this spec directly by pointing at
// `<server.public_url>/api/openapi.json`.
//
// The spec is built dynamically from the live config + dispatcher so tool
// enable/disable toggles and `server.public_url` are always reflected. No
// external OpenAPI library is pulled in — we hand-shape the document as
// `map[string]any` because the surface is small and stable.

package server

import (
	"net/http"

	"github.com/jet/pagefault/internal/dispatcher"
)

// buildOpenAPISpec returns a JSON-serializable OpenAPI 3.1.0 document for
// the currently-enabled pf_* REST tools. Callers pass the resolved public
// base URL (empty string if unset — the spec falls back to "/").
func buildOpenAPISpec(version, publicURL string, d *dispatcher.ToolDispatcher) map[string]any {
	if publicURL == "" {
		publicURL = "/"
	}

	paths := map[string]any{}
	if d.ToolEnabled("pf_maps") {
		paths["/api/pf_maps"] = pathItem("pf_maps", "List memory regions (contexts) exposed by pagefault.",
			openapiRef("ListContextsInput"), openapiRef("ListContextsOutput"))
	}
	if d.ToolEnabled("pf_load") {
		paths["/api/pf_load"] = pathItem("pf_load", "Load a named context bundle, optionally overriding format.",
			openapiRef("GetContextInput"), openapiRef("GetContextOutput"))
	}
	if d.ToolEnabled("pf_scan") {
		paths["/api/pf_scan"] = pathItem("pf_scan", "Search backends for content matching a query.",
			openapiRef("SearchInput"), openapiRef("SearchOutput"))
	}
	if d.ToolEnabled("pf_peek") {
		paths["/api/pf_peek"] = pathItem("pf_peek", "Read a single resource by URI.",
			openapiRef("ReadInput"), openapiRef("ReadOutput"))
	}
	if d.ToolEnabled("pf_fault") {
		paths["/api/pf_fault"] = pathItem("pf_fault", "Spawn a subagent to perform a deep retrieval.",
			openapiRef("DeepRetrieveInput"), openapiRef("DeepRetrieveOutput"))
	}
	if d.ToolEnabled("pf_ps") {
		paths["/api/pf_ps"] = pathItem("pf_ps", "List configured subagents.",
			openapiRef("ListAgentsInput"), openapiRef("ListAgentsOutput"))
	}
	if d.ToolEnabled("pf_poke") {
		paths["/api/pf_poke"] = pathItem("pf_poke", "Poke content back into memory (direct append or agent writeback).",
			openapiRef("WriteInput"), openapiRef("WriteOutput"))
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "pagefault",
			"description": "Config-driven memory server. REST counterpart of the MCP transport.",
			"version":     version,
		},
		"servers": []any{
			map[string]any{"url": publicURL},
		},
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"BearerAuth": map[string]any{
					"type":   "http",
					"scheme": "bearer",
				},
			},
			"schemas": openapiSchemas(),
		},
		"security": []any{
			map[string]any{"BearerAuth": []any{}},
		},
	}
}

// pathItem builds a POST-only path item entry. Every pf_* tool accepts a
// JSON body and returns JSON on success; shared 4xx/5xx error shapes come
// from the ErrorEnvelope schema.
func pathItem(opID, summary string, requestRef, responseRef map[string]any) map[string]any {
	return map[string]any{
		"post": map[string]any{
			"operationId": opID,
			"summary":     summary,
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": requestRef,
					},
				},
			},
			"responses": map[string]any{
				"200": map[string]any{
					"description": "Success",
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": responseRef,
						},
					},
				},
				"400": errorResponse("Invalid request"),
				"401": errorResponse("Missing or invalid bearer token"),
				"403": errorResponse("Request blocked by filter or untrusted proxy"),
				"404": errorResponse("Unknown context / resource / agent"),
				"413": errorResponse("Write payload exceeds backend max_entry_size"),
				"429": errorResponse("Rate limit exceeded"),
				"502": errorResponse("Backend unreachable"),
				"504": errorResponse("Subagent timed out"),
			},
		},
	}
}

// errorResponse is the shared response descriptor for non-2xx entries.
func errorResponse(desc string) map[string]any {
	return map[string]any{
		"description": desc,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": openapiRef("ErrorEnvelope"),
			},
		},
	}
}

// openapiRef builds a `$ref` pointer into components.schemas.
func openapiRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

// openapiSchemas returns the schemas block for every pf_* tool plus the
// shared error envelope. Field names are kept aligned with the JSON tags
// on the Go request/response structs in internal/tool.
func openapiSchemas() map[string]any {
	stringProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	intProp := func(desc string) map[string]any {
		return map[string]any{"type": "integer", "description": desc}
	}
	boolProp := func(desc string) map[string]any {
		return map[string]any{"type": "boolean", "description": desc}
	}
	numberProp := func(desc string) map[string]any {
		return map[string]any{"type": "number", "description": desc}
	}
	stringArray := func(desc string) map[string]any {
		return map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": desc,
		}
	}

	return map[string]any{
		"ErrorEnvelope": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code":    stringProp("Stable snake_case error code (e.g. invalid_request, resource_not_found)"),
						"status":  intProp("HTTP status code"),
						"message": stringProp("Human-readable error message"),
					},
					"required": []any{"code", "status", "message"},
				},
			},
			"required": []any{"error"},
		},

		// pf_maps
		"ListContextsInput": map[string]any{
			"type":        "object",
			"description": "Empty body — pf_maps takes no arguments.",
		},
		"ListContextsOutput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"contexts": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":        stringProp("Context name (unique)"),
							"description": stringProp("Human-readable description"),
						},
					},
				},
			},
			"required": []any{"contexts"},
		},

		// pf_load
		"GetContextInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":   stringProp("Context name to load"),
				"format": stringProp("Output format: markdown (default), markdown-with-metadata, or json"),
			},
			"required": []any{"name"},
		},
		"GetContextOutput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    stringProp("Context name"),
				"format":  stringProp("Format the content was rendered in"),
				"content": stringProp("Concatenated / structured content of the bundle"),
				"skipped_sources": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"uri":    stringProp("Source URI that was omitted"),
							"reason": stringProp("Why it was skipped"),
						},
					},
				},
			},
			"required": []any{"name", "format", "content"},
		},

		// pf_scan
		"SearchInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    stringProp("Free-text query"),
				"limit":    intProp("Maximum number of results (default 10)"),
				"backends": stringArray("Restrict to these backend names (default: all)"),
				"date_range": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"from": stringProp("ISO-8601 start date"),
						"to":   stringProp("ISO-8601 end date"),
					},
					"description": "Accepted for forward compatibility; ignored by current backends.",
				},
			},
			"required": []any{"query"},
		},
		"SearchOutput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"results": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"uri":      stringProp("Resource URI"),
							"snippet":  stringProp("Match excerpt"),
							"score":    map[string]any{"type": "number", "description": "Relevance score (nullable)"},
							"metadata": map[string]any{"type": "object", "additionalProperties": true},
							"backend":  stringProp("Name of the backend that produced the hit"),
						},
					},
				},
			},
			"required": []any{"results"},
		},

		// pf_peek
		"ReadInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uri":       stringProp("Resource URI (e.g. memory://foo.md)"),
				"from_line": intProp("Optional 1-indexed inclusive slice start"),
				"to_line":   intProp("Optional 1-indexed inclusive slice end"),
			},
			"required": []any{"uri"},
		},
		"ReadOutput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"uri":          stringProp("Resource URI"),
						"content":      stringProp("Body text"),
						"content_type": stringProp("IANA media type"),
						"metadata":     map[string]any{"type": "object", "additionalProperties": true},
					},
				},
			},
			"required": []any{"resource"},
		},

		// pf_fault
		"DeepRetrieveInput": map[string]any{
			"type":        "object",
			"description": "Async by default (0.10.0+): returns {task_id, status:\"running\"} immediately and the caller polls pf_ps(task_id=...). Set wait:true for synchronous behavior.",
			"properties": map[string]any{
				"query":            stringProp("Natural-language task for the subagent — what to find or understand. Include concrete entity names, topics, and dates where the user supplied them."),
				"agent":            stringProp("Agent id (default: first configured; see pf_ps for the list)"),
				"timeout_seconds":  intProp("Per-call timeout in seconds; default 120. On timeout the poll response carries status:\"timed_out\" and whatever partial output the subagent produced."),
				"time_range_start": stringProp("Optional free-form earliest date/time. Passed through to the subagent via the prompt template's {time_range} placeholder; pagefault does not parse the value."),
				"time_range_end":   stringProp("Optional free-form latest date/time. Same rules as time_range_start."),
				"wait":             boolProp("0.10.0+. Sync compatibility flag. Default (false) returns immediately with {task_id, status:\"running\"} and the caller polls pf_ps(task_id=...). Set to true to block until the task is terminal and return the full answer inline. MCP agent clients should leave this unset and use the polling pattern (30s × 6)."),
			},
			"required": []any{"query"},
		},
		"DeepRetrieveOutput": map[string]any{
			"type":        "object",
			"description": "Task snapshot. In async mode (wait omitted) only task_id/status/agent/backend/spawn_id are populated. In sync mode (wait:true) the terminal fields (answer, elapsed_seconds, etc.) are populated as well.",
			"properties": map[string]any{
				"task_id":         stringProp("pf_tk_* task identifier. Always populated — feed into pf_ps(task_id=...) to poll (async mode) or correlate with audit entries (sync mode)."),
				"status":          stringProp("Task lifecycle state: \"running\", \"done\", \"failed\", or \"timed_out\"."),
				"agent":           stringProp("Subagent id the task is running."),
				"backend":         stringProp("Subagent backend name."),
				"spawn_id":        stringProp("pf_sp_* random token minted per call. Included in the response so callers can correlate against downstream session logs when the operator wired {spawn_id} into their subagent command template."),
				"answer":          stringProp("Subagent response on success (populated when status is terminal)."),
				"elapsed_seconds": numberProp("Wall-clock elapsed seconds from task submission to terminal state."),
				"timed_out":       boolProp("True when status is \"timed_out\"."),
				"partial_result":  stringProp("Stdout captured before a timeout fired."),
				"error":           stringProp("Stringified failure message when status is \"failed\"."),
			},
			"required": []any{"task_id", "status", "agent", "backend"},
		},

		// pf_ps — polymorphic: agent list when task_id is empty, task
		// snapshot otherwise. OpenAPI does not model the polymorphism
		// cleanly, so we document both shapes in the description.
		"ListAgentsInput": map[string]any{
			"type":        "object",
			"description": "Empty body lists configured agents (Mode A). Set task_id to poll a pf_fault task snapshot (Mode B, 0.10.0+) — the response then matches DeepRetrieveOutput instead of ListAgentsOutput.",
			"properties": map[string]any{
				"task_id": stringProp("0.10.0+. Task id from a previous pf_fault call. When set, pf_ps returns a DeepRetrieveOutput snapshot (status, answer, elapsed, ...) instead of the agent list. Unknown or TTL-expired ids return 404 resource_not_found."),
			},
		},
		"ListAgentsOutput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agents": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":          stringProp("Agent id"),
							"description": stringProp("Agent description"),
							"backend":     stringProp("Hosting backend name"),
						},
					},
				},
			},
			"required": []any{"agents"},
		},

		// pf_poke
		"WriteInput": map[string]any{
			"type":        "object",
			"description": "Write content back to memory. Two modes: direct (append to uri) and agent (delegate to a subagent).",
			"properties": map[string]any{
				"uri":             stringProp("Target URI (required for mode:direct)"),
				"content":         stringProp("Content to persist"),
				"mode":            stringProp("\"direct\" | \"agent\""),
				"format":          stringProp("mode:direct only — \"entry\" (default) or \"raw\""),
				"agent":           stringProp("mode:agent only — subagent id (default: first configured)"),
				"target":          stringProp("mode:agent only — free-form target hint (\"auto\", \"daily\", \"long-term\", …)"),
				"timeout_seconds": intProp("mode:agent only — per-call deadline; default 120"),
			},
			"required": []any{"content", "mode"},
		},
		"WriteOutput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status":          stringProp("\"written\" on success"),
				"mode":            stringProp("Echoes the input mode (\"direct\" | \"agent\")"),
				"uri":             stringProp("Target URI (direct mode)"),
				"bytes_written":   intProp("Bytes that hit disk (direct mode)"),
				"format":          stringProp("Resolved format (direct mode; \"entry\" or \"raw\")"),
				"backend":         stringProp("Name of the backend that wrote (direct) or spawned (agent)"),
				"agent":           stringProp("Agent id (agent mode)"),
				"elapsed_seconds": map[string]any{"type": "number", "description": "Wall-clock elapsed (agent mode)"},
				"result":          stringProp("Subagent's textual response (agent mode)"),
				"targets_written": stringArray("URIs the subagent reports writing (agent mode; may be empty)"),
				"timed_out":       map[string]any{"type": "boolean", "description": "True if the subagent deadline fired (agent mode)"},
			},
			"required": []any{"status", "mode"},
		},
	}
}

// handleOpenAPISpec serves the live OpenAPI document for the REST
// transport. The endpoint is public (no auth) so importers like ChatGPT
// Custom GPT Actions can fetch the schema without supplying a bearer
// token first.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	spec := buildOpenAPISpec(Version, s.cfg.Server.PublicURL, s.dispatcher)
	writeJSON(w, http.StatusOK, spec)
}
