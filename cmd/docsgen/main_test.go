package main

import (
	"reflect"
	"testing"
)

// expectedCommandPaths is the canonical list of command paths the manifest
// must contain. Updating this list is a deliberate signal that the cobra
// surface area changed and docs need to be re-reviewed.
var expectedCommandPaths = [][]string{
	{"justtunnel"},
	{"justtunnel", "auth"},
	{"justtunnel", "logout"},
	{"justtunnel", "status"},
	{"justtunnel", "version"},
	{"justtunnel", "context"},
	{"justtunnel", "context", "list"},
	{"justtunnel", "context", "use"},
	{"justtunnel", "context", "show"},
	{"justtunnel", "worker"},
	{"justtunnel", "worker", "create"},
	{"justtunnel", "worker", "install"},
	{"justtunnel", "worker", "list"},
	{"justtunnel", "worker", "logs"},
	{"justtunnel", "worker", "rm"},
	{"justtunnel", "worker", "start"},
	{"justtunnel", "worker", "status"},
	{"justtunnel", "worker", "uninstall"},
}

func mustBuildManifest(t *testing.T) manifest {
	t.Helper()
	doc, err := BuildManifest()
	if err != nil {
		t.Fatalf("BuildManifest() returned error: %v", err)
	}
	return doc
}

func TestManifestEntryCount(t *testing.T) {
	doc := mustBuildManifest(t)
	const want = 18
	if got := len(doc.Commands); got != want {
		t.Fatalf("manifest entry count: got %d, want %d (paths: %v)", got, want, collectPaths(doc))
	}
}

func TestRootCommandPresent(t *testing.T) {
	doc := mustBuildManifest(t)
	for _, entry := range doc.Commands {
		if reflect.DeepEqual(entry.Path, []string{"justtunnel"}) {
			return
		}
	}
	t.Fatalf("root command path [justtunnel] not present in manifest; paths: %v", collectPaths(doc))
}

func TestRootFlagCount(t *testing.T) {
	doc := mustBuildManifest(t)
	var rootEntry *commandEntry
	for index := range doc.Commands {
		if reflect.DeepEqual(doc.Commands[index].Path, []string{"justtunnel"}) {
			rootEntry = &doc.Commands[index]
			break
		}
	}
	if rootEntry == nil {
		t.Fatalf("root entry missing")
	}
	// Root currently declares 8 user-facing flags:
	//   6 local non-persistent: subdomain, password, log-level,
	//     local-timeout, max-reconnect-attempts, config-file
	//   2 persistent: config, context
	// Cobra's auto --help is added at parse time and not present here.
	// This lower bound catches regressions where a declared flag goes
	// missing without forcing churn when a new flag is intentionally added.
	const minFlags = 8
	if got := len(rootEntry.Flags); got < minFlags {
		t.Fatalf("root flag count: got %d, want >= %d (flags: %+v)", got, minFlags, rootEntry.Flags)
	}
}

func TestExpectedLeafCommands(t *testing.T) {
	doc := mustBuildManifest(t)
	present := make(map[string]bool, len(doc.Commands))
	for _, entry := range doc.Commands {
		present[pathKey(entry.Path)] = true
	}
	for _, expected := range expectedCommandPaths {
		if !present[pathKey(expected)] {
			t.Errorf("expected command path %v missing from manifest", expected)
		}
	}
}

func TestEveryFlagHasNonEmptyFields(t *testing.T) {
	doc := mustBuildManifest(t)
	for _, entry := range doc.Commands {
		for _, flag := range entry.Flags {
			if flag.Name == "" {
				t.Errorf("command %v has flag with empty name: %+v", entry.Path, flag)
			}
			if flag.Type == "" {
				t.Errorf("command %v flag %q has empty type", entry.Path, flag.Name)
			}
			if flag.Description == "" {
				t.Errorf("command %v flag %q has empty description", entry.Path, flag.Name)
			}
		}
	}
}

func TestVersionFieldNonEmpty(t *testing.T) {
	doc := mustBuildManifest(t)
	if doc.Version == "" {
		t.Fatalf("manifest.Version is empty; expected version source to provide a non-empty value")
	}
}

func collectPaths(doc manifest) []string {
	paths := make([]string, 0, len(doc.Commands))
	for _, entry := range doc.Commands {
		paths = append(paths, pathKey(entry.Path))
	}
	return paths
}

func pathKey(path []string) string {
	key := ""
	for index, segment := range path {
		if index > 0 {
			key += " "
		}
		key += segment
	}
	return key
}
