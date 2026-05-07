// Command docsgen walks the cobra command tree exposed by cmd.RootCmd()
// and emits a JSON manifest describing every command and flag.
//
// The manifest is consumed by justtunnel-landing to render CLI Reference
// flag tables at build time. We import the command tree directly rather
// than exec'ing the binary so the manifest stays in sync with the source
// at compile time.
//
// Emission policy: ONE entry per command — leaves AND group parents
// (`context`, `worker`). Group parents have no flags of their own but
// the entry exists so docs can render group-level prose. Total entries
// for the current tree: 18 (16 leaves + 2 group parents).
//
// Usage:
//
//	go run ./cmd/docsgen           # writes JSON to stdout
//	go build ./cmd/docsgen         # builds ./docsgen binary
//
// Errors are written to stderr with exit code 1.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/justtunnel/justtunnel-cli/cmd"
	"github.com/justtunnel/justtunnel-cli/internal/version"
)

// flagEntry describes a single cobra/pflag flag in a form suitable for
// rendering as a documentation table row.
type flagEntry struct {
	Name        string `json:"name"`
	Shorthand   string `json:"shorthand"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// commandEntry describes a single command node in the cobra tree.
//
// Path is the dot-walk from the root command, e.g. ["justtunnel", "worker", "create"].
// Flags excludes inherited persistent flags, except on the root entry where
// the root's persistent flags are included (they apply to every invocation
// of the root command itself).
type commandEntry struct {
	Path     []string    `json:"path"`
	Use      string      `json:"use"`
	Short    string      `json:"short"`
	Long     string      `json:"long"`
	Flags    []flagEntry `json:"flags"`
	Examples []string    `json:"examples"`
}

// manifest is the top-level JSON document emitted to stdout.
type manifest struct {
	Version     string         `json:"version"`
	GeneratedAt string         `json:"generated_at"`
	Commands    []commandEntry `json:"commands"`
}

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "docsgen: %v\n", err)
		os.Exit(1)
	}
}

func run(out *os.File) error {
	doc, err := BuildManifest()
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	return nil
}

// BuildManifest walks the cobra command tree exposed by cmd.RootCmd()
// and returns a fully populated manifest. Exposed for testing so that
// assertions about command/flag coverage can be made without re-parsing
// the JSON output.
func BuildManifest() (manifest, error) {
	root := cmd.RootCmd()
	if root == nil {
		return manifest{}, fmt.Errorf("cmd.RootCmd() returned nil")
	}

	entries := make([]commandEntry, 0, 32)
	walk(root, []string{root.Name()}, &entries)

	return manifest{
		Version:     version.Version,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Commands:    entries,
	}, nil
}

// walk recursively appends an entry for `current` and each of its
// subcommands. Hidden cobra-internal commands (`help`, `completion`)
// are skipped so the manifest only contains user-facing surface area.
func walk(current *cobra.Command, path []string, entries *[]commandEntry) {
	*entries = append(*entries, buildEntry(current, path))

	for _, child := range current.Commands() {
		if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
			continue
		}
		childPath := make([]string, len(path)+1)
		copy(childPath, path)
		childPath[len(path)] = child.Name()
		walk(child, childPath, entries)
	}
}

// buildEntry materializes a single commandEntry from a cobra command.
//
// Flag selection:
//   - For the root command we include both local flags and persistent
//     flags, since persistent flags ARE flags the user can pass to the
//     bare root invocation.
//   - For subcommands we include only local flags (cmd.Flags() minus
//     inherited). Persistent flags from ancestors are documented on the
//     ancestor entry.
func buildEntry(current *cobra.Command, path []string) commandEntry {
	flags := make([]flagEntry, 0, 8)

	if current.HasParent() {
		// Local-only flags on subcommands. cmd.LocalFlags() returns
		// flags defined on this command (both Flags and PersistentFlags
		// declared on it directly), excluding inherited persistent flags.
		current.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			flags = append(flags, toFlagEntry(flag))
		})
	} else {
		// Root: walk Flags() (local non-persistent) AND PersistentFlags()
		// separately and merge — cobra's Flags() does NOT include persistent
		// flags declared on the same command, only inherited ones (and root
		// has nothing to inherit). Both are user-passable on the bare root
		// invocation.
		seen := make(map[string]struct{})
		current.Flags().VisitAll(func(flag *pflag.Flag) {
			if _, dup := seen[flag.Name]; dup {
				return
			}
			seen[flag.Name] = struct{}{}
			flags = append(flags, toFlagEntry(flag))
		})
		current.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
			if _, dup := seen[flag.Name]; dup {
				return
			}
			seen[flag.Name] = struct{}{}
			flags = append(flags, toFlagEntry(flag))
		})
	}

	return commandEntry{
		Path:     path,
		Use:      current.Use,
		Short:    current.Short,
		Long:     current.Long,
		Flags:    flags,
		Examples: []string{},
	}
}

func toFlagEntry(flag *pflag.Flag) flagEntry {
	return flagEntry{
		Name:        flag.Name,
		Shorthand:   flag.Shorthand,
		Type:        flag.Value.Type(),
		Default:     flag.DefValue,
		Description: flag.Usage,
	}
}
