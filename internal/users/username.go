package users

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	UsernameMinLen = 3
	UsernameMaxLen = 40

	// UsernameChangeCooldown is how long a user must wait between changes.
	// The first change is free — see Service.UpdateMe.
	UsernameChangeCooldown = 14 * 24 * time.Hour
)

// The character set is deliberately narrow. Usernames are headed for a public
// profile URL (/u/<username>), so anything that needs escaping there is
// rejected here instead.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// reservedUsernames are names the product needs to keep, either because they
// imply authority or because they would shadow a real route. Compared
// case-insensitively.
var reservedUsernames = map[string]bool{
	"about": true, "admin": true, "administrator": true, "api": true,
	"help": true, "klyvi": true, "login": true, "privacy": true,
	"profile": true, "root": true, "settings": true, "signin": true,
	"signup": true, "support": true, "system": true, "terms": true,
	"www": true,
}

// Reasons reported by the availability endpoint when a name cannot be used.
const (
	ReasonInvalid  = "invalid"
	ReasonReserved = "reserved"
	ReasonTaken    = "taken"
)

var (
	ErrUsernameInvalid  = errors.New("username must be 3..40 characters, using letters, numbers, underscores, and hyphens")
	ErrUsernameReserved = errors.New("that username is not available")
	ErrUsernameTaken    = errors.New("that username is taken")
)

// UsernameCooldownError reports that a username was changed too recently.
// It carries the deadline so the client can disable the field and say when
// the next change unlocks rather than failing a save the user committed to.
type UsernameCooldownError struct {
	RetryAt time.Time
}

func (e *UsernameCooldownError) Error() string {
	return fmt.Sprintf("username can be changed again on %s", e.RetryAt.UTC().Format(time.RFC3339))
}

// ValidateUsername checks format first, then reservation. Format wins when a
// name fails both, because that is the problem the user can act on.
func ValidateUsername(name string) error {
	n := utf8.RuneCountInString(name)
	if n < UsernameMinLen || n > UsernameMaxLen {
		return ErrUsernameInvalid
	}
	if !usernamePattern.MatchString(name) {
		return ErrUsernameInvalid
	}
	if reservedUsernames[strings.ToLower(name)] {
		return ErrUsernameReserved
	}
	return nil
}

// UnavailableReason maps a username error to the reason string the
// availability endpoint reports. Returns "" for a nil error.
func UnavailableReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrUsernameReserved):
		return ReasonReserved
	case errors.Is(err, ErrUsernameTaken):
		return ReasonTaken
	default:
		return ReasonInvalid
	}
}
