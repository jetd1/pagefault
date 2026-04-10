package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/model"
	"github.com/jet/pagefault/internal/tool"
)

// cliCaller is the fixed Caller identity attributed to `pagefault <tool>`
// invocations. The operator is a single logical principal — filters and
// audit entries use this identity so CLI calls are distinguishable from
// remote clients in the audit log.
var cliCaller = model.Caller{ID: "cli", Label: "pagefault CLI"}

// resolveConfigPath returns the effective config path, searching in order:
//  1. the explicit --config flag
//  2. the PAGEFAULT_CONFIG environment variable
//  3. ./pagefault.yaml in the current directory
//
// An error is returned only if none of the candidates exist on disk.
func resolveConfigPath(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if env := os.Getenv("PAGEFAULT_CONFIG"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "./pagefault.yaml")

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("pagefault config not found (tried: %s — pass --config or set $PAGEFAULT_CONFIG)",
		strings.Join(candidates, ", "))
}

// loadDispatcherForCLI resolves a config, loads it, optionally disables
// the filter pipeline (operator opt-out), re-routes any stdout-mode audit
// to stderr so CLI output stays clean for pipelines, and returns a
// fully-wired ToolDispatcher plus a closer for the audit logger.
func loadDispatcherForCLI(configPath string, noFilter bool) (*dispatcher.ToolDispatcher, func() error, error) {
	resolved, err := resolveConfigPath(configPath)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(resolved)
	if err != nil {
		return nil, nil, err
	}
	if noFilter {
		// Wipe the filter config before building the dispatcher so
		// the dispatcher installs a pass-through filter.
		cfg.Filters = config.FiltersConfig{Enabled: false}
	}
	// CLI invocations share stdout with the tool output, so a
	// stdout-mode audit logger would pollute pipes like `pagefault load
	// demo --json | jq`. Rewrite stdout → stderr so audit still fires
	// (and shows up on an interactive terminal) without contaminating
	// the data path. File-backed sinks are untouched.
	if cfg.Audit.Mode == "stdout" {
		cfg.Audit.Mode = "stderr"
	}
	return buildDispatcher(cfg)
}

// registerCommonFlags adds --config, --no-filter, and --json to a FlagSet
// and returns pointers the caller reads after fs.Parse.
func registerCommonFlags(fs *flag.FlagSet) (configPath *string, noFilter *bool, asJSON *bool) {
	configPath = fs.String("config", "", "path to pagefault.yaml (falls back to $PAGEFAULT_CONFIG, then ./pagefault.yaml)")
	noFilter = fs.Bool("no-filter", false, "bypass the filter pipeline (operator override)")
	asJSON = fs.Bool("json", false, "emit JSON instead of human-readable output")
	return
}

// parseInterspersed parses args against fs but allows positional arguments
// to appear in any position. Go's stdlib flag package stops at the first
// non-flag token, which is a footgun for subcommands that take positional
// args — `pagefault peek memory://foo.md --from 5` would silently drop
// `--from`. This wrapper walks the raw args, hoists every recognised flag
// (and its value) to the front, then delegates to fs.Parse.
//
// The `--` terminator is honoured: everything after it is treated as
// positional, matching POSIX convention.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var flagArgs, positional []string
	terminated := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if terminated {
			positional = append(positional, a)
			continue
		}
		if a == "--" {
			terminated = true
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}

		// Strip leading dashes and split on '=' to recover the flag name.
		name := strings.TrimLeft(a, "-")
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
			hasValue = true
		}
		f := fs.Lookup(name)
		if f == nil {
			// Unknown flag — hand it to fs.Parse so it can emit the
			// standard error message. Everything from this arg onward
			// goes into flagArgs unchanged.
			flagArgs = append(flagArgs, args[i:]...)
			break
		}

		flagArgs = append(flagArgs, a)
		if hasValue {
			continue
		}
		// Boolean flags take no value; every other flag consumes the
		// next token.
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return fs.Parse(append(flagArgs, positional...))
}

// ─────────────────── maps (pf_maps) ───────────────────

// runMaps implements `pagefault maps` — the CLI face of pf_maps. It lists
// every configured memory region (context) with its description.
func runMaps(args []string) error {
	fs := flag.NewFlagSet("maps", flag.ContinueOnError)
	configPath, noFilter, asJSON := registerCommonFlags(fs)
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}

	d, closer, err := loadDispatcherForCLI(*configPath, *noFilter)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	out, err := tool.HandleListContexts(context.Background(), d, tool.ListContextsInput{}, cliCaller)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(out)
	}
	if len(out.Contexts) == 0 {
		fmt.Println("(no contexts configured)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDESCRIPTION")
	for _, c := range out.Contexts {
		fmt.Fprintf(tw, "%s\t%s\n", c.Name, c.Description)
	}
	return tw.Flush()
}

// ─────────────────── load (pf_load) ───────────────────

// runLoad implements `pagefault load <name>` — the CLI face of pf_load. It
// fetches a named region's assembled content and prints it to stdout. Any
// skipped sources are reported on stderr so they don't contaminate a pipe.
func runLoad(args []string) error {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	configPath, noFilter, asJSON := registerCommonFlags(fs)
	format := fs.String("format", "", "output format: markdown | json (default from config)")
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: pagefault load <name> [--config PATH] [--format FMT] [--no-filter] [--json]")
	}
	name := fs.Arg(0)

	d, closer, err := loadDispatcherForCLI(*configPath, *noFilter)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	out, err := tool.HandleGetContext(context.Background(), d,
		tool.GetContextInput{Name: name, Format: *format}, cliCaller)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(out)
	}

	fmt.Print(out.Content)
	if !strings.HasSuffix(out.Content, "\n") {
		fmt.Println()
	}
	if len(out.SkippedSources) > 0 {
		fmt.Fprintf(os.Stderr, "\n(%d source(s) skipped:\n", len(out.SkippedSources))
		for _, s := range out.SkippedSources {
			fmt.Fprintf(os.Stderr, "  %s — %s\n", s.URI, s.Reason)
		}
		fmt.Fprintln(os.Stderr, ")")
	}
	return nil
}

// ─────────────────── scan (pf_scan) ───────────────────

// runScan implements `pagefault scan <query>` — the CLI face of pf_scan.
// Multiple positional args are joined with spaces so `pagefault scan hello
// world` works without quoting.
func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	configPath, noFilter, asJSON := registerCommonFlags(fs)
	limit := fs.Int("limit", 10, "maximum number of results")
	backendsCSV := fs.String("backends", "", "comma-separated backend names to restrict to (default: all)")
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: pagefault scan <query> [--config PATH] [--limit N] [--backends a,b] [--no-filter] [--json]")
	}
	query := strings.Join(fs.Args(), " ")

	var backends []string
	if *backendsCSV != "" {
		for _, b := range strings.Split(*backendsCSV, ",") {
			if b = strings.TrimSpace(b); b != "" {
				backends = append(backends, b)
			}
		}
	}

	d, closer, err := loadDispatcherForCLI(*configPath, *noFilter)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	out, err := tool.HandleSearch(context.Background(), d, tool.SearchInput{
		Query:    query,
		Limit:    *limit,
		Backends: backends,
	}, cliCaller)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(out)
	}
	if len(out.Results) == 0 {
		fmt.Println("(no matches)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BACKEND\tURI\tSNIPPET")
	for _, r := range out.Results {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Backend, r.URI, singleLine(r.Snippet))
	}
	return tw.Flush()
}

// ─────────────────── peek (pf_peek) ───────────────────

// runPeek implements `pagefault peek <uri>` — the CLI face of pf_peek. The
// resource content is written to stdout; an optional line range slices a
// region before printing.
func runPeek(args []string) error {
	fs := flag.NewFlagSet("peek", flag.ContinueOnError)
	configPath, noFilter, asJSON := registerCommonFlags(fs)
	fromLine := fs.Int("from", 0, "start line (1-indexed, inclusive)")
	toLine := fs.Int("to", 0, "end line (1-indexed, inclusive)")
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: pagefault peek <uri> [--config PATH] [--from N] [--to N] [--no-filter] [--json]")
	}
	uri := fs.Arg(0)

	d, closer, err := loadDispatcherForCLI(*configPath, *noFilter)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	out, err := tool.HandleRead(context.Background(), d, tool.ReadInput{
		URI:      uri,
		FromLine: *fromLine,
		ToLine:   *toLine,
	}, cliCaller)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(out)
	}
	if out.Resource == nil {
		return errors.New("no resource returned")
	}
	fmt.Print(out.Resource.Content)
	if !strings.HasSuffix(out.Resource.Content, "\n") {
		fmt.Println()
	}
	return nil
}

// ─────────────────── fault (pf_fault) ───────────────────

// runFault implements `pagefault fault <query>` — the CLI face of
// pf_fault. It spawns a subagent to perform deep retrieval and prints
// the answer (or the partial result + a notice on timeout).
func runFault(args []string) error {
	fs := flag.NewFlagSet("fault", flag.ContinueOnError)
	configPath, noFilter, asJSON := registerCommonFlags(fs)
	agent := fs.String("agent", "", "subagent id to spawn (see `pagefault ps`; empty picks the first configured)")
	timeoutSec := fs.Int("timeout", 120, "max seconds to wait for the agent")
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: pagefault fault <query...> [--config PATH] [--agent ID] [--timeout N] [--no-filter] [--json]")
	}
	query := strings.Join(fs.Args(), " ")

	d, closer, err := loadDispatcherForCLI(*configPath, *noFilter)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	out, err := tool.HandleDeepRetrieve(context.Background(), d, tool.DeepRetrieveInput{
		Query:          query,
		Agent:          *agent,
		TimeoutSeconds: *timeoutSec,
	}, cliCaller)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(out)
	}

	if out.TimedOut {
		fmt.Fprintf(os.Stderr, "(timed out after %.1fs — showing partial result)\n", out.ElapsedSeconds)
		fmt.Print(out.PartialResult)
		if !strings.HasSuffix(out.PartialResult, "\n") {
			fmt.Println()
		}
		return nil
	}
	fmt.Print(out.Answer)
	if !strings.HasSuffix(out.Answer, "\n") {
		fmt.Println()
	}
	fmt.Fprintf(os.Stderr, "\n(%s/%s, %.1fs)\n", out.Backend, out.Agent, out.ElapsedSeconds)
	return nil
}

// ─────────────────── ps (pf_ps) ───────────────────

// runPs implements `pagefault ps` — the CLI face of pf_ps. It lists
// every subagent exposed by every configured SubagentBackend.
func runPs(args []string) error {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	configPath, noFilter, asJSON := registerCommonFlags(fs)
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}

	d, closer, err := loadDispatcherForCLI(*configPath, *noFilter)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	out, err := tool.HandleListAgents(context.Background(), d, tool.ListAgentsInput{}, cliCaller)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(out)
	}
	if len(out.Agents) == 0 {
		fmt.Println("(no subagents configured)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tBACKEND\tDESCRIPTION")
	for _, a := range out.Agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", a.ID, a.Backend, a.Description)
	}
	return tw.Flush()
}

// ─────────────────── helpers ───────────────────

// printJSON writes v to stdout as indented JSON. Used for --json output
// across every tool subcommand.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// singleLine collapses newlines and tabs in a snippet so it fits on one
// tabwriter row.
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}
