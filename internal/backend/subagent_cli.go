package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/model"
)

// SubagentCLIBackend spawns an external CLI process to do deep retrieval.
// It fits the SubagentBackend interface used by pf_fault and by
// pf_poke mode:"agent".
//
// The command template is tokenized once at construction. Each token is a
// separate argv element — there is no shell interpretation, so a user-
// supplied {task} cannot escape into shell metacharacters. Tokens are
// written as they appear in the config; quoted tokens (single or double)
// are stripped before substitution.
//
// Example config:
//
//	command: "openclaw agent run --agent {agent_id} --task {task} --timeout {timeout}"
//
// At Spawn time, {agent_id}, {task}, {timeout} in each token are replaced
// with the actual values. The {task} substitution happens *after* the
// raw SpawnRequest.Task has been wrapped with the resolved prompt
// template (per-agent override → backend default → built-in for the
// purpose), so the subprocess sees a fully-framed prompt rather than
// a bare user query.
type SubagentCLIBackend struct {
	name       string
	argv       []string // tokenized command template
	timeoutSec int
	agents     []AgentInfo
	defaultID  string

	// Prompt-template wiring. retrieveTmpl / writeTmpl are the
	// backend-level defaults (empty fields fall through to the
	// built-ins in prompt.go). agentTmpl holds per-agent overrides
	// keyed on agent id.
	retrieveTmpl string
	writeTmpl    string
	agentTmpl    map[string]agentTemplates

	// execCommand is indirected for tests so they can substitute a fake
	// process (typically `go test -run ... -helper`).
	execCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewSubagentCLIBackend builds a CLI subagent backend from config.
func NewSubagentCLIBackend(cfg *config.SubagentCLIBackendConfig) (*SubagentCLIBackend, error) {
	if cfg == nil {
		return nil, errors.New("subagent-cli backend: nil config")
	}
	argv, err := tokenizeCommand(cfg.Command)
	if err != nil {
		return nil, fmt.Errorf("subagent-cli backend %q: %w", cfg.Name, err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("subagent-cli backend %q: empty command template", cfg.Name)
	}
	if len(cfg.Agents) == 0 {
		return nil, fmt.Errorf("subagent-cli backend %q: no agents configured", cfg.Name)
	}
	agents := make([]AgentInfo, 0, len(cfg.Agents))
	agentTmpl := make(map[string]agentTemplates, len(cfg.Agents))
	seen := make(map[string]bool, len(cfg.Agents))
	for _, a := range cfg.Agents {
		if a.ID == "" {
			return nil, fmt.Errorf("subagent-cli backend %q: agent with empty id", cfg.Name)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("subagent-cli backend %q: duplicate agent id %q", cfg.Name, a.ID)
		}
		seen[a.ID] = true
		agents = append(agents, AgentInfo{ID: a.ID, Description: a.Description})
		agentTmpl[a.ID] = agentTemplates{
			RetrievePromptTemplate: a.RetrievePromptTemplate,
			WritePromptTemplate:    a.WritePromptTemplate,
		}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 300
	}
	return &SubagentCLIBackend{
		name:         cfg.Name,
		argv:         argv,
		timeoutSec:   timeout,
		agents:       agents,
		defaultID:    agents[0].ID,
		retrieveTmpl: cfg.RetrievePromptTemplate,
		writeTmpl:    cfg.WritePromptTemplate,
		agentTmpl:    agentTmpl,
		execCommand:  exec.CommandContext,
	}, nil
}

// Name returns the backend name.
func (b *SubagentCLIBackend) Name() string { return b.name }

// Read is not meaningful for a subagent backend — callers should use
// pf_fault to spawn an agent instead. Always returns ErrResourceNotFound.
func (b *SubagentCLIBackend) Read(ctx context.Context, uri string) (*Resource, error) {
	return nil, fmt.Errorf("%w: subagent backend %q cannot Read URIs (use pf_fault)",
		model.ErrResourceNotFound, b.name)
}

// Search is a noop for subagent backends. Returns an empty slice.
func (b *SubagentCLIBackend) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	return nil, nil
}

// ListResources is a noop for subagent backends. Returns an empty slice.
func (b *SubagentCLIBackend) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	return nil, nil
}

// ListAgents returns the configured agents.
func (b *SubagentCLIBackend) ListAgents() []AgentInfo {
	out := make([]AgentInfo, len(b.agents))
	copy(out, b.agents)
	return out
}

// DefaultAgentID returns the id of the first configured agent, used when
// a Spawn call passes an empty agentID.
func (b *SubagentCLIBackend) DefaultAgentID() string { return b.defaultID }

// Spawn runs the configured command for the requested agent with the
// given task. The returned string is the agent's stdout (trimmed of
// trailing newline). If the timeout fires before the process exits,
// Spawn returns (partial, wrapped ErrSubagentTimeout) — callers may
// surface the partial result if it is useful.
//
// Before the argv {task} substitution, req.Task is wrapped with the
// resolved prompt template (per-agent override → backend default →
// built-in for req.Purpose) via WrapTask. This is the layer that
// teaches a fresh subagent "you are a memory-retrieval agent, search
// the user's memory, do not answer from your own world knowledge" so
// the operator does not have to hand-roll the framing in every
// caller.
func (b *SubagentCLIBackend) Spawn(ctx context.Context, req SpawnRequest) (string, error) {
	agentID := req.AgentID
	if agentID == "" {
		agentID = b.defaultID
	}
	if !hasAgentID(b.agents, agentID) {
		return "", fmt.Errorf("%w: %q on backend %q", model.ErrAgentNotFound, agentID, b.name)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = time.Duration(b.timeoutSec) * time.Second
	}

	// Resolve the effective prompt template and wrap the raw task.
	// An empty agentID at this point has already been replaced with
	// the backend default, so agentPromptOverride sees the concrete
	// id.
	purpose := req.Purpose
	if purpose == "" {
		purpose = SpawnPurposeRetrieve
	}
	agentOverride := agentPromptOverride(b.agentTmpl, agentID, purpose)
	backendDefault := b.retrieveTmpl
	if purpose == SpawnPurposeWrite {
		backendDefault = b.writeTmpl
	}
	tmpl := ResolvePromptTemplate(agentOverride, backendDefault, purpose)
	wrapped := WrapTask(tmpl, SpawnRequest{
		AgentID:   agentID,
		Task:      req.Task,
		Purpose:   purpose,
		TimeRange: req.TimeRange,
		Target:    req.Target,
	})

	// Substitute placeholders in the pre-tokenized argv. {task} is the
	// fully-wrapped prompt; {agent_id} and {timeout} come through
	// from the request.
	args := make([]string, len(b.argv))
	timeoutStr := fmt.Sprintf("%d", int(timeout.Seconds()))
	for i, tok := range b.argv {
		tok = strings.ReplaceAll(tok, "{agent_id}", agentID)
		tok = strings.ReplaceAll(tok, "{task}", wrapped)
		tok = strings.ReplaceAll(tok, "{timeout}", timeoutStr)
		args[i] = tok
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := b.execCommand(runCtx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimRight(stdout.String(), "\n")

	// Distinguish timeout (runCtx.Err()) from regular exit errors. When
	// CommandContext kills the process on deadline, cmd.Run returns a
	// "signal: killed" error and runCtx.Err() is context.DeadlineExceeded.
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%w: agent %q on backend %q timed out after %s",
			model.ErrSubagentTimeout, agentID, b.name, timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return out, ctx.Err()
	}
	if err != nil {
		stderrMsg := strings.TrimSpace(stderr.String())
		if stderrMsg != "" {
			return out, fmt.Errorf("subagent %q on backend %q: %w: %s", agentID, b.name, err, stderrMsg)
		}
		return out, fmt.Errorf("subagent %q on backend %q: %w", agentID, b.name, err)
	}
	return out, nil
}

// tokenizeCommand splits a command-template string into argv tokens,
// honouring single and double quotes. Quotes are stripped from the
// returned tokens. Backslash escapes are not interpreted (keeps the
// grammar small) — if an operator needs exotic characters, they can use
// the unquoted form or wrap in a shell script.
func tokenizeCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	started := false

	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case (c == ' ' || c == '\t' || c == '\n') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if inSingle || inDouble {
		return nil, errors.New("unterminated quote in command template")
	}
	flush()
	return out, nil
}
