package users_test

import (
	"errors"
	"strings"
	"testing"

	"klyvi-api/internal/users"
)

// ValidateUsername is the single gate all three call sites share (create,
// update, availability), so its edge cases are worth pinning down here
// rather than rediscovering them per endpoint.
func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		// Length boundaries. Counted in runes, not bytes.
		{"two chars is too short", "ab", users.ErrUsernameInvalid},
		{"three chars is the floor", "abc", nil},
		{"forty chars is the ceiling", strings.Repeat("a", 40), nil},
		{"forty-one chars is too long", strings.Repeat("a", 41), users.ErrUsernameInvalid},
		{"empty", "", users.ErrUsernameInvalid},

		// Allowed character set.
		{"letters", "jacob", nil},
		{"mixed case", "Jacob", nil},
		{"digits", "user2024", nil},
		{"underscore", "jacob_k", nil},
		{"hyphen", "jacob-k", nil},
		{"all allowed classes", "Jacob_K-99", nil},

		// Disallowed characters. These matter because usernames are headed
		// for a public profile URL.
		{"space", "jacob k", users.ErrUsernameInvalid},
		{"dot", "jacob.k", users.ErrUsernameInvalid},
		{"slash", "jacob/k", users.ErrUsernameInvalid},
		{"at sign", "jacob@k", users.ErrUsernameInvalid},
		{"percent", "jacob%20k", users.ErrUsernameInvalid},

		// Multi-byte input: 7 runes, 21 bytes. A byte-counting length check
		// would reject this for the wrong reason; the pattern is what should
		// reject it.
		{"multi-byte within rune length", "日本語ユーザー", users.ErrUsernameInvalid},
		{"emoji", "jacob🎬", users.ErrUsernameInvalid},

		// Reserved names, matched case-insensitively.
		{"reserved admin", "admin", users.ErrUsernameReserved},
		{"reserved api", "api", users.ErrUsernameReserved},
		{"reserved klyvi", "klyvi", users.ErrUsernameReserved},
		{"reserved settings", "settings", users.ErrUsernameReserved},
		{"reserved upper case", "ADMIN", users.ErrUsernameReserved},
		{"reserved mixed case", "AdMiN", users.ErrUsernameReserved},

		// A reserved word as a substring is fine; only the whole name is held.
		{"reserved as prefix", "administrator2", nil},
		{"reserved as suffix", "notadmin", nil},

		// Format is checked before reservation: a name that is both too short
		// and reserved reports the format problem, which is the one the user
		// can act on.
		{"short and reserved", "ap", users.ErrUsernameInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := users.ValidateUsername(tt.input)
			if !errors.Is(err, tt.want) {
				t.Errorf("ValidateUsername(%q) = %v, want %v", tt.input, err, tt.want)
			}
		})
	}
}

// The availability endpoint reports why a name is unusable, so the reason
// string has to line up with the error the validator returns.
func TestUnavailableReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"invalid", users.ErrUsernameInvalid, users.ReasonInvalid},
		{"reserved", users.ErrUsernameReserved, users.ReasonReserved},
		{"taken", users.ErrUsernameTaken, users.ReasonTaken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := users.UnavailableReason(tt.err); got != tt.want {
				t.Errorf("UnavailableReason(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}

	if got := users.UnavailableReason(nil); got != "" {
		t.Errorf("UnavailableReason(nil) = %q, want empty", got)
	}
}
