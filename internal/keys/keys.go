// Package keys manages the on-disk API key store (~/secrets/keys.json).
package keys

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Name identifies a key slot in the store.
type Name string

const (
	Exa       Name = "exa"
	Parallel  Name = "parallel"
	Sonar     Name = "sonar"
	Youcom    Name = "youcom"
	Brave     Name = "brave"
	Tavily    Name = "tavily"
	Linkup    Name = "linkup"
	Firecrawl Name = "firecrawl"
	Keenable  Name = "keenable"
	SerpBase  Name = "serpbase"
	Serply    Name = "serply"
	SearXNG   Name = "searxng"
	Degoog    Name = "degoog"
	Jev       Name = "jev"
)

// spec is what the CLI needs to know about one slot.
type spec struct {
	env, display, url string
	isURL             bool // the slot holds an instance URL, not a secret
}

var specs = map[Name]spec{
	Exa:       {"EXA_API_KEY", "Exa", "https://dashboard.exa.ai/api-keys", false},
	Parallel:  {"PARALLEL_API_KEY", "Parallel", "https://platform.parallel.ai", false},
	Sonar:     {"SONAR_API_KEY", "Sonar (Perplexity)", "https://perplexity.ai", false},
	Youcom:    {"YOUCOM_API_KEY", "You.com", "https://you.com/platform/api-keys", false},
	Brave:     {"BRAVE_API_KEY", "Brave Search (recommended: 5,000 free searches/month)", "https://brave.com/search/api/", false},
	Tavily:    {"TAVILY_API_KEY", "Tavily", "https://app.tavily.com", false},
	Linkup:    {"LINKUP_API_KEY", "Linkup", "https://app.linkup.so", false},
	Firecrawl: {"FIRECRAWL_API_KEY", "Firecrawl", "https://www.firecrawl.dev", false},
	Keenable:  {"KEENABLE_API_KEY", "Keenable", "https://keenable.ai", false},
	SerpBase:  {"SERPBASE_API_KEY", "SerpBase", "https://serpbase.dev", false},
	Serply:    {"SERPLY_API_KEY", "Serply", "https://serply.io", false},
	SearXNG:   {"SEARXNG_URL", "SearXNG URL", "https://docs.searxng.org", true},
	Degoog:    {"DEGOOG_URL", "Degoog URL", "https://github.com/degoog-org/degoog", true},
	Jev:       {"JEV_API_KEY", "Jev (TypeSafe)", "https://typesafe.ai", false},
}

// SearchProviders lists the slots that correspond to search providers, in
// the order setup offers them. SearXNG and Degoog hold instance URLs rather
// than secrets; DuckDuckGo and ketch need nothing and have no slot.
var SearchProviders = []Name{Brave, Exa, Parallel, Sonar, Youcom, Tavily, Linkup, Firecrawl, Keenable, SerpBase, Serply, SearXNG, Degoog}

// All lists every key name, search providers first.
var All = append(append([]Name{}, SearchProviders...), Jev)

// Secret reports whether the slot holds a credential that should be masked.
func (n Name) Secret() bool { return !specs[n].isURL }

// IsURL reports whether the slot holds an instance URL.
func (n Name) IsURL() bool { return specs[n].isURL }

// EnvVar returns the environment variable that overrides this key.
func (n Name) EnvVar() string { return specs[n].env }

// Display returns a human-friendly label for the key.
func (n Name) Display() string {
	if sp, ok := specs[n]; ok {
		return sp.display
	}
	return string(n)
}

// URL returns where a user can obtain the key.
func (n Name) URL() string { return specs[n].url }

// Parse converts a user-supplied string into a Name.
func Parse(s string) (Name, error) {
	for _, n := range All {
		if string(n) == s {
			return n, nil
		}
	}
	names := make([]string, len(All))
	for i, n := range All {
		names[i] = string(n)
	}
	return "", fmt.Errorf("unknown key %q (expected one of %s)", s, strings.Join(names, ", "))
}

// Store is the JSON shape of keys.json. Empty strings mean "not configured".
type Store struct {
	ExaAPIKey       string `json:"exa_api_key"`
	ParallelAPIKey  string `json:"parallel_api_key"`
	SonarAPIKey     string `json:"sonar_api_key"`
	YoucomAPIKey    string `json:"youcom_api_key,omitempty"`
	BraveAPIKey     string `json:"brave_api_key,omitempty"`
	TavilyAPIKey    string `json:"tavily_api_key,omitempty"`
	LinkupAPIKey    string `json:"linkup_api_key,omitempty"`
	FirecrawlAPIKey string `json:"firecrawl_api_key,omitempty"`
	KeenableAPIKey  string `json:"keenable_api_key,omitempty"`
	SerpBaseAPIKey  string `json:"serpbase_api_key,omitempty"`
	SerplyAPIKey    string `json:"serply_api_key,omitempty"`
	SearXNGURL      string `json:"searxng_url"`
	DegoogURL       string `json:"degoog_url,omitempty"`
	JevAPIKey       string `json:"jev_api_key"`
}

// slot returns the field that holds name, or nil.
func (s *Store) slot(name Name) *string {
	switch name {
	case Exa:
		return &s.ExaAPIKey
	case Parallel:
		return &s.ParallelAPIKey
	case Sonar:
		return &s.SonarAPIKey
	case Youcom:
		return &s.YoucomAPIKey
	case Brave:
		return &s.BraveAPIKey
	case Tavily:
		return &s.TavilyAPIKey
	case Linkup:
		return &s.LinkupAPIKey
	case Firecrawl:
		return &s.FirecrawlAPIKey
	case Keenable:
		return &s.KeenableAPIKey
	case SerpBase:
		return &s.SerpBaseAPIKey
	case Serply:
		return &s.SerplyAPIKey
	case SearXNG:
		return &s.SearXNGURL
	case Degoog:
		return &s.DegoogURL
	case Jev:
		return &s.JevAPIKey
	}
	return nil
}

// DefaultDir returns ~/webctl, the config directory.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "webctl"), nil
}

// DefaultPath returns ~/secrets/keys.json. The file may be shared with other
// tools; Save preserves fields it does not know.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "secrets", "keys.json"), nil
}

// Load reads the store at path. A missing file yields an empty store, not an error.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w (fix the JSON or delete the file and re-run `webctl setup`)", path, err)
	}
	return &s, nil
}

// Save writes the store to path with 0600 permissions, creating the parent dir
// (0700) if needed. Fields already in the file that Store does not define are
// kept, so a keys file shared with other tools is not clobbered.
func (s *Store) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	data, err := s.merged(path)
	if err != nil {
		return err
	}

	// Write to a temp file then rename so a crash never leaves a half-written keys.json.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keys-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

// merged returns the JSON to write: the file's existing fields, if any,
// overlaid with this store's. Empty values are written explicitly so an
// unset key reads back as unset.
func (s *Store) merged(path string) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if existing, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(existing, &fields); err != nil {
			return nil, fmt.Errorf("parse %s: %w (fix the JSON or delete the file)", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	own, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encode keys: %w", err)
	}
	var ownFields map[string]json.RawMessage
	if err := json.Unmarshal(own, &ownFields); err != nil {
		return nil, fmt.Errorf("encode keys: %w", err)
	}
	for k, v := range ownFields {
		fields[k] = v
	}
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode keys: %w", err)
	}
	return append(data, '\n'), nil
}

// Get returns the key for name.
func (s *Store) Get(name Name) string {
	if p := s.slot(name); p != nil {
		return *p
	}
	return ""
}

// Set assigns the key for name.
func (s *Store) Set(name Name, value string) {
	if p := s.slot(name); p != nil {
		*p = value
	}
}

// Has reports whether a non-empty key is configured for name.
func (s *Store) Has(name Name) bool { return s.Get(name) != "" }

// ConfiguredProviders returns the search providers with a non-empty key.
func (s *Store) ConfiguredProviders() []Name {
	var out []Name
	for _, n := range SearchProviders {
		if s.Has(n) {
			out = append(out, n)
		}
	}
	return out
}

// Mask returns a redacted preview of a key suitable for display (e.g. "sk-1…f9c2").
func Mask(key string) string {
	if key == "" {
		return "(not set)"
	}
	if strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://") {
		return key // URLs are not secrets
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "…" + key[len(key)-4:]
}
