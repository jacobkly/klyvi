// Package errlog records errors from the API and the web client in the
// database, grouped so the same failure in the same place is one row with a
// count. It is the app's own error tracker: no third party sees the data.
package errlog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Sources an error can come from.
const (
	SourceAPI = "api"
	SourceWeb = "web"
)

// Retention is how long a group is kept after it was last seen.
const Retention = 30 * 24 * time.Hour

// Field caps, in bytes.
const (
	maxKind    = 100
	maxMessage = 1000
	maxStack   = 8000
	maxRoute   = 300
	maxAnchor  = 300
)

// Event is one occurrence of an error.
type Event struct {
	Source  string
	Kind    string // "panic", an HTTP status like "500", or a JS error name
	Message string
	Stack   string
	Route   string // the route pattern, the page path, or a background job's name
	Method  string
	Status  int
	UserID  *uuid.UUID
	// Anchor is what makes two events the same error: the function that
	// panicked, the top stack frame, or a client's error digest. When empty,
	// the method and route stand in.
	Anchor string
}

// Fingerprint groups events: the same source, kind and anchor are one error.
func (e Event) Fingerprint() string {
	anchor := e.Anchor
	if anchor == "" {
		anchor = e.Method + " " + e.Route
	}
	sum := sha256.Sum256([]byte(e.Source + "|" + e.Kind + "|" + anchor))
	return hex.EncodeToString(sum[:16])
}

// clean scrubs secrets and trims every field to its cap.
func (e Event) clean() Event {
	e.Kind = trim(strings.TrimSpace(e.Kind), maxKind)
	e.Message = trim(Scrub(strings.TrimSpace(e.Message)), maxMessage)
	e.Stack = trim(Scrub(e.Stack), maxStack)
	e.Route = trim(Scrub(e.Route), maxRoute)
	e.Anchor = trim(Scrub(e.Anchor), maxAnchor)
	return e
}

// trim cuts s to at most n bytes without splitting a character.
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

const redacted = "[redacted]"

var scrubbers = []struct {
	re   *regexp.Regexp
	repl string
}{
	// JWTs, Supabase's included.
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`), redacted},
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}" + redacted},
	// Secrets in query strings and key=value text.
	{regexp.MustCompile(`(?i)\b([a-z_]*(?:token|key|secret|password|code)=)[^&\s"']+`), "${1}" + redacted},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), redacted},
	// Stripe and Supabase style keys.
	{regexp.MustCompile(`\b(?:sk|pk|sb|rk)_[A-Za-z0-9_]{16,}`), redacted},
}

// Scrub replaces tokens, keys and emails in s with [redacted].
func Scrub(s string) string {
	for _, sc := range scrubbers {
		s = sc.re.ReplaceAllString(s, sc.repl)
	}
	return s
}

// PanicEvent builds an event from a recovered panic and its stack. The
// anchor is the function that panicked, so the same panic groups across
// requests and deploys (line numbers and arguments aren't part of it).
func PanicEvent(rec any, stack []byte) Event {
	return Event{
		Source:  SourceAPI,
		Kind:    "panic",
		Message: fmt.Sprint(rec),
		Stack:   string(stack),
		Anchor:  panicSite(stack),
	}
}

// panicSite finds the first function below the panic call in a
// debug.Stack() dump that isn't part of the runtime.
func panicSite(stack []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(stack))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	afterPanic := false
	for sc.Scan() {
		line := sc.Text()
		// Function lines aren't indented; their file:line follows, indented.
		if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "goroutine ") {
			continue
		}
		fn := line
		if i := strings.LastIndex(fn, "("); i > 0 {
			fn = fn[:i]
		}
		if strings.HasPrefix(fn, "panic") {
			afterPanic = true
			continue
		}
		if !afterPanic || strings.HasPrefix(fn, "runtime.") {
			continue
		}
		return fn
	}
	return ""
}
