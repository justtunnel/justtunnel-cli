package config

import (
	"fmt"
	"strings"
)

// PersonalContext is the default context name used when no team is selected.
const PersonalContext = "personal"

// TeamContextPrefix is the prefix that identifies a team-scoped context.
// Team contexts have the form "team:<slug>".
const TeamContextPrefix = "team:"

// ValidateContext returns nil if name is a syntactically valid context name.
// Valid forms:
//   - "personal"
//   - "team:<id-or-slug>" where the trailing value is non-empty and contains
//     only [A-Za-z0-9-]. The CLI accepts both the team's slug ("dev-team")
//     and the team's ULID-shaped _id ("01KQTJBVA6REFPMKT8MPKX8Z9N") because
//     the server route currently keys workers by the ULID, not the slug
//     (see justtunnel-cli#43).
//
// It does NOT validate that the user actually has access to the named context.
func ValidateContext(name string) error {
	if name == PersonalContext {
		return nil
	}
	if !strings.HasPrefix(name, TeamContextPrefix) {
		return fmt.Errorf("invalid context %q: must be %q or %q<id-or-slug>", name, PersonalContext, TeamContextPrefix)
	}
	identifier := strings.TrimPrefix(name, TeamContextPrefix)
	if identifier == "" {
		return fmt.Errorf("invalid context %q: team identifier is empty", name)
	}
	for _, character := range identifier {
		isLower := character >= 'a' && character <= 'z'
		isUpper := character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		isDash := character == '-'
		if !isLower && !isUpper && !isDigit && !isDash {
			return fmt.Errorf("invalid context %q: team identifier may only contain letters, digits, and dashes", name)
		}
	}
	return nil
}

// SetCurrentContext validates the context name and persists it to the config file.
// The provided cfg is mutated in place so callers can continue using it.
func SetCurrentContext(cfg *Config, name string) error {
	if err := ValidateContext(name); err != nil {
		return err
	}
	cfg.CurrentContext = name
	if err := Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// ResolveContext returns the effective context for the current invocation.
// Precedence: explicit flag value > config CurrentContext > PersonalContext.
// flagValue should be the literal value of the --context flag ("" if unset).
func ResolveContext(cfg *Config, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if cfg != nil && cfg.CurrentContext != "" {
		return cfg.CurrentContext
	}
	return PersonalContext
}
