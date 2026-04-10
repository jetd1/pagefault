// Package config defines the pagefault YAML configuration schema and provides
// loading, environment-variable substitution, and validation.
//
// The server is a pure runtime for a Config value — every behavior (backends,
// contexts, tools, filters, auth, audit) is driven from a YAML file.
//
// A Config is loaded via Load, which reads the file, expands ${ENV_VAR}
// references using os.ExpandEnv, unmarshals it into a Config struct, applies
// defaults, and validates the result with go-playground/validator.
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Config is the root of the pagefault configuration.
type Config struct {
	Server   ServerConfig    `yaml:"server" validate:"required"`
	Auth     AuthConfig      `yaml:"auth" validate:"required"`
	Backends []BackendConfig `yaml:"backends" validate:"required,min=1,dive"`
	Contexts []ContextConfig `yaml:"contexts" validate:"dive"`
	Tools    ToolsConfig     `yaml:"tools"`
	Filters  FiltersConfig   `yaml:"filters"`
	Audit    AuditConfig     `yaml:"audit"`
}

// ServerConfig configures the HTTP listener.
type ServerConfig struct {
	Host      string `yaml:"host" validate:"required"`
	Port      int    `yaml:"port" validate:"required,gt=0,lt=65536"`
	PublicURL string `yaml:"public_url,omitempty"`
}

// AuthConfig configures the authentication layer.
//
// Mode selects the provider: "none", "bearer", or "trusted_header".
type AuthConfig struct {
	Mode          string              `yaml:"mode" validate:"required,oneof=none bearer trusted_header"`
	Bearer        BearerAuthConfig    `yaml:"bearer,omitempty"`
	TrustedHeader TrustedHeaderConfig `yaml:"trusted_header,omitempty"`
}

// BearerAuthConfig configures bearer-token authentication.
type BearerAuthConfig struct {
	// TokensFile is a JSONL file; each line is a token record:
	//   {"id": "...", "token": "pf_...", "label": "..."}
	TokensFile string `yaml:"tokens_file,omitempty"`
}

// TrustedHeaderConfig configures trusted-header authentication (behind a
// reverse proxy that has already authenticated the caller).
type TrustedHeaderConfig struct {
	Header         string   `yaml:"header,omitempty"`
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
}

// BackendConfig is a generic backend configuration. Type-specific fields are
// decoded into a type-specific struct by the backend registry.
//
// The Raw field preserves the full YAML node so backends can decode their own
// fields without every backend type polluting this struct.
type BackendConfig struct {
	Name string `yaml:"name" validate:"required"`
	Type string `yaml:"type" validate:"required"`

	// Raw is the full YAML node for this backend entry. Backends unmarshal
	// their type-specific fields from this node.
	Raw yaml.Node `yaml:"-"`
}

// UnmarshalYAML captures the full node so backends can decode their own fields.
func (b *BackendConfig) UnmarshalYAML(value *yaml.Node) error {
	// Decode the common fields.
	type rawBackend struct {
		Name string `yaml:"name"`
		Type string `yaml:"type"`
	}
	var rb rawBackend
	if err := value.Decode(&rb); err != nil {
		return err
	}
	b.Name = rb.Name
	b.Type = rb.Type
	b.Raw = *value
	return nil
}

// FilesystemBackendConfig is the configuration for a filesystem backend.
// It is decoded from BackendConfig.Raw by the filesystem backend constructor.
type FilesystemBackendConfig struct {
	Name      string              `yaml:"name" validate:"required"`
	Type      string              `yaml:"type" validate:"required,eq=filesystem"`
	Root      string              `yaml:"root" validate:"required"`
	Include   []string            `yaml:"include"`
	Exclude   []string            `yaml:"exclude"`
	URIScheme string              `yaml:"uri_scheme" validate:"required"`
	AutoTag   map[string][]string `yaml:"auto_tag,omitempty"`
	Sandbox   bool                `yaml:"sandbox"`
}

// ContextConfig defines a named bundle of backend sources.
type ContextConfig struct {
	Name        string          `yaml:"name" validate:"required"`
	Description string          `yaml:"description,omitempty"`
	Sources     []ContextSource `yaml:"sources" validate:"required,min=1,dive"`
	Format      string          `yaml:"format,omitempty"`   // "markdown" | "json"
	MaxSize     int             `yaml:"max_size,omitempty"` // characters
}

// ContextSource points to a resource on a named backend.
type ContextSource struct {
	Backend string         `yaml:"backend" validate:"required"`
	URI     string         `yaml:"uri" validate:"required"`
	Params  map[string]any `yaml:"params,omitempty"`
}

// ToolsConfig toggles individual tools on or off.
//
// All *bool-valued fields default to enabled (true) when absent. Using *bool
// lets us distinguish "not set" from "set to false".
type ToolsConfig struct {
	ListContexts *bool `yaml:"list_contexts,omitempty"`
	GetContext   *bool `yaml:"get_context,omitempty"`
	Search       *bool `yaml:"search,omitempty"`
	Read         *bool `yaml:"read,omitempty"`
	DeepRetrieve *bool `yaml:"deep_retrieve,omitempty"`
	ListAgents   *bool `yaml:"list_agents,omitempty"`
	Write        *bool `yaml:"write,omitempty"`
}

// Enabled returns whether the named tool is enabled. Unknown names default to
// disabled. Unset tools default to enabled.
func (t ToolsConfig) Enabled(name string) bool {
	pick := func(p *bool) bool {
		if p == nil {
			return true
		}
		return *p
	}
	switch name {
	case "list_contexts":
		return pick(t.ListContexts)
	case "get_context":
		return pick(t.GetContext)
	case "search":
		return pick(t.Search)
	case "read":
		return pick(t.Read)
	case "deep_retrieve":
		return pick(t.DeepRetrieve)
	case "list_agents":
		return pick(t.ListAgents)
	case "write":
		return pick(t.Write)
	default:
		return false
	}
}

// FiltersConfig holds the optional filter pipeline configuration.
type FiltersConfig struct {
	Enabled   bool             `yaml:"enabled"`
	Path      PathFilterConfig `yaml:"path,omitempty"`
	Tags      TagFilterConfig  `yaml:"tags,omitempty"`
	Redaction RedactionConfig  `yaml:"redaction,omitempty"`
}

// PathFilterConfig configures URI allow/deny globs.
type PathFilterConfig struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// TagFilterConfig configures tag allow/deny sets.
type TagFilterConfig struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// RedactionConfig configures the Phase-3 redaction filter.
type RedactionConfig struct {
	Enabled bool            `yaml:"enabled"`
	Rules   []RedactionRule `yaml:"rules,omitempty"`
}

// RedactionRule is a single regex → replacement rule.
type RedactionRule struct {
	Pattern     string `yaml:"pattern" validate:"required"`
	Replacement string `yaml:"replacement"`
}

// AuditConfig configures the audit logger.
type AuditConfig struct {
	Enabled        bool   `yaml:"enabled"`
	LogPath        string `yaml:"log_path,omitempty"`        // "jsonl" mode
	Mode           string `yaml:"mode,omitempty"`            // "jsonl" | "stdout" | "off"
	IncludeContent bool   `yaml:"include_content,omitempty"` // include full result in audit (warning: large)
}

// ErrValidation wraps a validation failure for callers that want to test with
// errors.Is.
var ErrValidation = errors.New("config validation failed")

// Load reads a YAML config file, expands ${ENV_VAR} references in its
// contents, unmarshals it into a *Config, applies defaults, and validates the
// result.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse parses YAML config bytes (with ${ENV} substitution applied) into a
// validated *Config.
func Parse(raw []byte) (*Config, error) {
	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate checks the config against validator tags and returns a wrapped
// ErrValidation on failure.
func (c *Config) Validate() error {
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(c); err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, err.Error())
	}
	return nil
}

// applyDefaults fills in default values for optional fields.
func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8444
	}
	if c.Audit.Mode == "" {
		if c.Audit.LogPath != "" {
			c.Audit.Mode = "jsonl"
		} else if c.Audit.Enabled {
			c.Audit.Mode = "stdout"
		} else {
			c.Audit.Mode = "off"
		}
	}
	for i := range c.Contexts {
		if c.Contexts[i].Format == "" {
			c.Contexts[i].Format = "markdown"
		}
		if c.Contexts[i].MaxSize == 0 {
			c.Contexts[i].MaxSize = 16000
		}
	}
}

// DecodeFilesystemBackend extracts a FilesystemBackendConfig from a generic
// BackendConfig. Returns an error if the type is not "filesystem" or if
// decoding fails.
func DecodeFilesystemBackend(bc BackendConfig) (*FilesystemBackendConfig, error) {
	if bc.Type != "filesystem" {
		return nil, fmt.Errorf("config: backend %q: expected type filesystem, got %q", bc.Name, bc.Type)
	}
	var fs FilesystemBackendConfig
	if err := bc.Raw.Decode(&fs); err != nil {
		return nil, fmt.Errorf("config: backend %q: decode filesystem: %w", bc.Name, err)
	}
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&fs); err != nil {
		return nil, fmt.Errorf("config: backend %q: %w: %s", bc.Name, ErrValidation, err.Error())
	}
	return &fs, nil
}
