package keys

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNameMetadata(t *testing.T) {
	for _, n := range All {
		if n.EnvVar() == "" || n.Display() == "" || n.URL() == "" {
			t.Errorf("%s is missing metadata: %q %q %q", n, n.EnvVar(), n.Display(), n.URL())
		}
	}
	if Exa.EnvVar() != "EXA_API_KEY" || Jev.EnvVar() != "JEV_API_KEY" {
		t.Error("env var names changed")
	}
	if Name("bogus").EnvVar() != "" || Name("bogus").Display() != "bogus" {
		t.Error("unknown names should degrade gracefully")
	}
	if len(SearchProviders) != 13 || len(All) != 14 || All[len(All)-1] != Jev {
		t.Errorf("SearchProviders=%v All=%v", SearchProviders, All)
	}
}

func TestParse(t *testing.T) {
	for _, n := range All {
		got, err := Parse(string(n))
		if err != nil || got != n {
			t.Errorf("Parse(%q) = %q, %v", n, got, err)
		}
	}
	if _, err := Parse("Exa"); err == nil {
		t.Error("Parse is case-sensitive; callers lowercase first")
	}
	if _, err := Parse("bing"); err == nil || !strings.Contains(err.Error(), "expected one of") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := DefaultDir()
	if err != nil || dir != filepath.Join(home, "webctl") {
		t.Errorf("DefaultDir = %q, %v", dir, err)
	}
	p, err := DefaultPath()
	if err != nil || p != filepath.Join(home, "secrets", "keys.json") {
		t.Errorf("DefaultPath = %q, %v", p, err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope", "keys.json"))
	if err != nil {
		t.Fatalf("missing file should yield an empty store, got %v", err)
	}
	if *s != (Store{}) {
		t.Errorf("store = %+v", s)
	}
}

func TestLoadBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "parse") || !strings.Contains(err.Error(), "webctl setup") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(`{"exa_api_key":"e","unknown_field":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get(Exa) != "e" || s.Has(Jev) {
		t.Errorf("store = %+v", s)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "webctl")
	path := filepath.Join(dir, "keys.json")

	s := &Store{}
	s.Set(Exa, "exa-1")
	s.Set(Jev, "jev-1")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if *loaded != *s {
		t.Errorf("round trip mismatch: %+v vs %+v", loaded, s)
	}

	// The file must contain every slot (empty strings for unset keys) so it is
	// self-documenting for manual editing.
	raw, _ := os.ReadFile(path)
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"exa_api_key", "parallel_api_key", "sonar_api_key", "jev_api_key"} {
		if _, ok := m[k]; !ok {
			t.Errorf("saved JSON missing %q: %s", k, raw)
		}
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("saved file should end with a newline")
	}

	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("keys.json mode = %o, want 0600", fi.Mode().Perm())
		}
		di, _ := os.Stat(dir)
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %o, want 0700", di.Mode().Perm())
		}
	}

	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".keys-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveOverwritesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	first := &Store{ExaAPIKey: "old"}
	if err := first.Save(path); err != nil {
		t.Fatal(err)
	}
	second := &Store{ExaAPIKey: "new", SonarAPIKey: "s"}
	if err := second.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, _ := Load(path)
	if loaded.ExaAPIKey != "new" || loaded.SonarAPIKey != "s" {
		t.Errorf("loaded = %+v", loaded)
	}
}

func TestSaveFailsWhenDirIsAFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := (&Store{}).Save(filepath.Join(blocker, "keys.json"))
	if err == nil {
		t.Error("expected error when parent path is a file")
	}
}

func TestGetSetHas(t *testing.T) {
	s := &Store{}
	for _, n := range All {
		if s.Has(n) {
			t.Errorf("%s should start empty", n)
		}
		s.Set(n, "v-"+string(n))
		if s.Get(n) != "v-"+string(n) || !s.Has(n) {
			t.Errorf("%s round trip failed", n)
		}
	}
	s.Set(Name("bogus"), "x")
	if s.Get(Name("bogus")) != "" {
		t.Error("unknown name should be ignored")
	}
}

func TestConfiguredProviders(t *testing.T) {
	s := &Store{SonarAPIKey: "s", ExaAPIKey: "e", JevAPIKey: "j"}
	got := s.ConfiguredProviders()
	if len(got) != 2 || got[0] != Exa || got[1] != Sonar {
		t.Errorf("ConfiguredProviders = %v, want [exa sonar] in canonical order (jev excluded)", got)
	}
	if (&Store{}).ConfiguredProviders() != nil {
		t.Error("empty store should return nil")
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                  "(not set)",
		"short":             "****",
		"12345678":          "****",
		"sk-live-abcdef9c2": "sk-l…f9c2",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSavePreservesForeignFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(`{"other_tool_token":"keep-me","jev_api_key":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Store{JevAPIKey: "new"}).Save(path); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["other_tool_token"] != "keep-me" || got["jev_api_key"] != "new" || got["exa_api_key"] != "" {
		t.Errorf("file = %v", got)
	}
}
