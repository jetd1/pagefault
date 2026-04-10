// Command pagefault is the entry point for the pagefault memory service.
//
// It supports the following subcommands:
//
//	pagefault serve --config <path> [--host HOST] [--port PORT]
//	pagefault token create --label <label> [--config <path>] [--tokens-file <path>]
//	pagefault token ls [--config <path>] [--tokens-file <path>]
//	pagefault token revoke <id> [--config <path>] [--tokens-file <path>]
//	pagefault --version
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

Usage:
  pagefault serve --config <path> [--host HOST] [--port PORT]
  pagefault token create --label <label> [--config <path>] [--tokens-file <path>]
  pagefault token ls [--config <path>] [--tokens-file <path>]
  pagefault token revoke <id> [--config <path>] [--tokens-file <path>]
  pagefault --version

Run "pagefault <command> --help" for more information on a command.`)
}
