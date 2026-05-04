package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestConfig(name string) *Config {
	return &Config{
		WorkerID:        "wkr_01HX0000000000000000000000",
		Name:            name,
		Context:         "team:acme",
		Subdomain:       name + "--acme",
		CreatedAt:       time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		ServiceBackend:  "launchd",
		ServiceUnitPath: "/Users/test/Library/LaunchAgents/dev.justtunnel.worker." + name + ".plist",
	}
}

func TestWorkerDir_CreatesDirWithCorrectPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	dir, err := WorkerDir()
	if err != nil {
		t.Fatalf("WorkerDir: %v", err)
	}
	want := filepath.Join(home, "workers")
	if dir != want {
		t.Fatalf("WorkerDir = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("not a directory")
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("dir perm = %o, want 0700", perm)
		}
	}
}

func TestWriteRead_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	names := []string{"alice", strings.Repeat("a", 63)}
	for _, name := range names {
		t.Run("name="+name, func(t *testing.T) {
			cfg := newTestConfig(name)
			if err := Write(cfg); err != nil {
				t.Fatalf("Write: %v", err)
			}

			got, err := Read(name)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got.WorkerID != cfg.WorkerID || got.Name != cfg.Name || got.Context != cfg.Context ||
				got.Subdomain != cfg.Subdomain || got.ServiceBackend != cfg.ServiceBackend ||
				got.ServiceUnitPath != cfg.ServiceUnitPath || !got.CreatedAt.Equal(cfg.CreatedAt) {
				t.Fatalf("round-trip mismatch: got %+v want %+v", got, cfg)
			}
		})
	}
}

func TestWrite_FilePermissions0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissions not enforced on windows")
	}
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	cfg := newTestConfig("alice")
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	path, err := ConfigPath("alice")
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file perm = %o, want 0600", perm)
	}
}

func TestWrite_IdempotentReinit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	cfg := newTestConfig("alice")
	if err := Write(cfg); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	path, _ := ConfigPath("alice")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read1: %v", err)
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read2: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("file changed on idempotent re-write:\nfirst:  %s\nsecond: %s", first, second)
	}
}

func TestWrite_NoLingeringTempFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	cfg := newTestConfig("alice")
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir, _ := WorkerDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("found lingering tmp file: %s", entry.Name())
		}
	}
}

func TestList_EmptyDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List on empty = %d configs, want 0", len(got))
	}
}

func TestList_ReturnsAllConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	names := []string{"alice", "bob", "carol"}
	for _, name := range names {
		if err := Write(newTestConfig(name)); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}
	// Drop a non-json file to ensure it's ignored.
	dir, _ := WorkerDir()
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(names) {
		t.Fatalf("List len = %d, want %d", len(got), len(names))
	}
	gotNames := make([]string, len(got))
	for idx, cfg := range got {
		gotNames[idx] = cfg.Name
	}
	sort.Strings(gotNames)
	for idx, want := range names {
		if gotNames[idx] != want {
			t.Fatalf("List[%d].Name = %q, want %q", idx, gotNames[idx], want)
		}
	}
}

func TestDelete_Idempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	if err := Write(newTestConfig("alice")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Delete("alice"); err != nil {
		t.Fatalf("Delete first: %v", err)
	}
	// Second delete on missing file is a no-op.
	if err := Delete("alice"); err != nil {
		t.Fatalf("Delete missing should be no-op, got: %v", err)
	}
	// Delete of a never-existed name is also a no-op.
	if err := Delete("never-existed"); err != nil {
		t.Fatalf("Delete never-existed should be no-op, got: %v", err)
	}
}

func TestRead_MissingReturnsErrNotExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	_, err := Read("ghost")
	if err == nil {
		t.Fatalf("Read missing should error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read missing err = %v, want errors.Is(os.ErrNotExist)", err)
	}
}

func TestNameValidation_RejectsBadNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	bad := []string{
		"",
		"../foo",
		"foo/bar",
		"foo\\bar",
		"-leading-dash",
		"UPPER",
		"with space",
		"dots.allowed?",
		strings.Repeat("a", 64), // too long
	}
	for _, name := range bad {
		t.Run("name="+name, func(t *testing.T) {
			if _, err := ConfigPath(name); err == nil {
				t.Fatalf("ConfigPath(%q) should reject", name)
			}
			if _, err := Read(name); err == nil {
				t.Fatalf("Read(%q) should reject", name)
			}
			if err := Delete(name); err == nil {
				t.Fatalf("Delete(%q) should reject", name)
			}
			cfg := newTestConfig("placeholder")
			cfg.Name = name
			if err := Write(cfg); err == nil {
				t.Fatalf("Write(%q) should reject", name)
			}
		})
	}
}

func TestNameValidation_AcceptsGoodNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	good := []string{
		"a",
		"alice",
		"alice-1",
		"0worker",
		strings.Repeat("a", 63), // boundary length
	}
	for _, name := range good {
		t.Run("name="+name, func(t *testing.T) {
			if _, err := ConfigPath(name); err != nil {
				t.Fatalf("ConfigPath(%q) should accept, got %v", name, err)
			}
		})
	}
}

func TestConcurrentWrites_NoCorruption(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	// Each goroutine writes a distinct payload (same Name so they all
	// race for the same file). After the dust settles the on-disk file
	// must be byte-identical to one of the writers (no torn writes).
	candidates := make([]*Config, 20)
	for idx := range candidates {
		cfg := newTestConfig("alice")
		cfg.WorkerID = "wkr_writer_" + strings.Repeat("x", idx+1)
		cfg.Subdomain = "alice--writer-" + strings.Repeat("x", idx+1)
		candidates[idx] = cfg
	}

	var waitGroup sync.WaitGroup
	for _, cfg := range candidates {
		waitGroup.Add(1)
		go func(cfg *Config) {
			defer waitGroup.Done()
			if err := Write(cfg); err != nil {
				t.Errorf("concurrent Write: %v", err)
			}
		}(cfg)
	}
	waitGroup.Wait()

	got, err := Read("alice")
	if err != nil {
		t.Fatalf("Read after concurrent writes: %v", err)
	}
	matched := false
	for _, candidate := range candidates {
		if reflect.DeepEqual(got, candidate) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("post-race config did not match any writer; got %+v", got)
	}
}

func TestRead_NameMismatchErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	// Write a file at alice.json whose Name field says "bob".
	dir, err := WorkerDir()
	if err != nil {
		t.Fatalf("WorkerDir: %v", err)
	}
	cfg := newTestConfig("bob")
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alice.json"), payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = Read("alice")
	if err == nil {
		t.Fatalf("Read should error on name mismatch")
	}
	if !strings.Contains(err.Error(), "mismatched name") {
		t.Fatalf("err = %v, want to mention mismatched name", err)
	}
}

func TestList_SkipsCorruptFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	if err := Write(newTestConfig("alice")); err != nil {
		t.Fatalf("Write alice: %v", err)
	}
	if err := Write(newTestConfig("carol")); err != nil {
		t.Fatalf("Write carol: %v", err)
	}
	dir, _ := WorkerDir()
	if err := os.WriteFile(filepath.Join(dir, "bob.json"), []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	got, err := List()
	if err != nil {
		t.Fatalf("List should not fail on corrupt file: %v", err)
	}
	gotNames := make([]string, len(got))
	for idx, cfg := range got {
		gotNames[idx] = cfg.Name
	}
	sort.Strings(gotNames)
	want := []string{"alice", "carol"}
	if !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("List names = %v, want %v", gotNames, want)
	}
}

func TestList_SkipsNameMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUSTTUNNEL_HOME", home)

	if err := Write(newTestConfig("alice")); err != nil {
		t.Fatalf("Write alice: %v", err)
	}
	dir, _ := WorkerDir()
	// File named bob.json but with Name="impostor" inside.
	mismatched := newTestConfig("impostor")
	payload, err := json.MarshalIndent(mismatched, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bob.json"), payload, 0o600); err != nil {
		t.Fatalf("write mismatched: %v", err)
	}

	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "alice" {
		t.Fatalf("List = %+v, want only alice", got)
	}
}

// TestValidateDerivedSubdomain exercises C6: the joined `<name>--<slug>`
// form must not exceed the 63-octet DNS label limit. Personal contexts
// (empty slug) skip the check because the bare name is already capped.
func TestValidateDerivedSubdomain(t *testing.T) {
	cases := []struct {
		label   string
		name    string
		slug    string
		wantErr bool
	}{
		{"personal short-circuits", "any-name-here", "", false},
		{"empty slug allowed", "build", "", false},
		{"comfortably under cap", "build", "acme", false},
		{
			label:   "exactly at cap (63 chars)",
			name:    strings.Repeat("a", 30),
			slug:    strings.Repeat("b", 31), // 30 + 2 + 31 = 63
			wantErr: false,
		},
		{
			label:   "one over cap (64 chars)",
			name:    strings.Repeat("a", 30),
			slug:    strings.Repeat("b", 32), // 30 + 2 + 32 = 64
			wantErr: true,
		},
		{
			label:   "wildly over cap",
			name:    strings.Repeat("a", 60),
			slug:    strings.Repeat("b", 60),
			wantErr: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(subTest *testing.T) {
			err := ValidateDerivedSubdomain(testCase.name, testCase.slug)
			if testCase.wantErr && err == nil {
				subTest.Fatalf("want error for %q + %q (joined len %d), got nil",
					testCase.name, testCase.slug, len(testCase.name)+2+len(testCase.slug))
			}
			if !testCase.wantErr && err != nil {
				subTest.Fatalf("want no error, got %v", err)
			}
			if testCase.wantErr && !strings.Contains(err.Error(), "DNS label") {
				subTest.Fatalf("error %q should mention DNS label", err)
			}
		})
	}
}
