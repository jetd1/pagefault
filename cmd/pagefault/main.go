// Command pagefault is the entry point for the pagefault memory service.
//
// It supports the following subcommands:
//
//	pagefault serve --config <path> [--host HOST] [--port PORT]
//	pagefault token create --label <label> [--config <path>] [--tokens-file <path>]
//	pagefault token ls                     [--config <path>] [--tokens-file <path>]
//	pagefault token revoke <id>            [--config <path>] [--tokens-file <path>]
//
//	pagefault maps        [--config] [--no-filter] [--json]
//	pagefault load <name> [--config] [--format markdown|json] [--no-filter] [--json]
//	pagefault scan <query...> [--config] [--limit N] [--backends a,b] [--no-filter] [--json]
//	pagefault peek <uri>  [--config] [--from N] [--to N] [--no-filter] [--json]
//	pagefault fault <query...> [--config] [--agent ID] [--timeout N] [--after DATE] [--before DATE] [--no-filter] [--json]
//	pagefault ps          [--config] [--no-filter] [--json]
//	pagefault poke [--mode direct|agent] [--uri URI] [--format entry|raw] <content...>
//
//	pagefault --version
//
// The tool subcommands (maps, load, scan, peek, fault, ps, poke) are
// the CLI form of the pf_maps / pf_load / pf_scan / pf_peek / pf_fault
// / pf_ps / pf_poke tools exposed over MCP and REST. See CLAUDE.md
// §Tool Naming for the wire ↔ CLI ↔ code mapping.
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "--version", "-v", "version":
		fmt.Println("pagefault", version)
		return
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
	case "token":
		if err := runToken(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "token: %v\n", err)
			os.Exit(1)
		}
	case "maps":
		if err := runMaps(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "maps: %v\n", err)
			os.Exit(1)
		}
	case "load":
		if err := runLoad(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "load: %v\n", err)
			os.Exit(1)
		}
	case "scan":
		if err := runScan(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			os.Exit(1)
		}
	case "peek":
		if err := runPeek(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "peek: %v\n", err)
			os.Exit(1)
		}
	case "fault":
		if err := runFault(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "fault: %v\n", err)
			os.Exit(1)
		}
	case "ps":
		if err := runPs(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ps: %v\n", err)
			os.Exit(1)
		}
	case "poke":
		if err := runPoke(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "poke: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `pagefault — personal memory service

Server:
  pagefault serve --config <path> [--host HOST] [--port PORT]

Tokens:
  pagefault token create --label <label> [--config <path>] [--tokens-file <path>]
  pagefault token ls                     [--config <path>] [--tokens-file <path>]
  pagefault token revoke <id>            [--config <path>] [--tokens-file <path>]

Tools (local CLI form of pf_maps / pf_load / pf_scan / pf_peek / pf_fault / pf_ps / pf_poke):
  pagefault maps                 — list configured memory regions
  pagefault load <name>          — load an assembled region to stdout
  pagefault scan <query...>      — scan backends for a query
  pagefault peek <uri>           — read a resource by URI
  pagefault fault <query...>     — spawn a subagent for deep retrieval
  pagefault ps                   — list configured subagents
  pagefault poke --mode direct|agent [--uri URI] <content...>
                                 — poke content back into memory (direct append or agent writeback)

  Common flags: --config <path>, --no-filter, --json
  Config lookup: --config → $PAGEFAULT_CONFIG → ./pagefault.yaml

  pagefault --version

Run "pagefault <command> --help" for per-command flags.`)
}
