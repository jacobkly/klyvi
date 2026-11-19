package errlog

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/response"
)

// API serves POST /v1/errors, where the web client reports its own errors.
type API struct {
	rec      *Recorder
	userFrom func(context.Context) (uuid.UUID, bool)
}

// NewAPI builds the handler. userFrom reads the signed-in user, if any, from
// the request context; the route is open to signed-out visitors too.
func NewAPI(rec *Recorder, userFrom func(context.Context) (uuid.UUID, bool)) *API {
	return &API{rec: rec, userFrom: userFrom}
}

type reportRequest struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Stack   string `json:"stack"`
	Page    string `json:"page"`
	Digest  string `json:"digest"`
}

// Report takes one error report. It answers 202 whether or not the report
// is kept, so a client can't tell when the cap is reached.
func (a *API) Report(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		response.WriteError(w, http.StatusBadRequest, "message is required")
		return
	}

	e := Event{
		Source:  SourceWeb,
		Kind:    req.Kind,
		Message: req.Message,
		Stack:   req.Stack,
		Route:   pagePath(req.Page),
		Anchor:  webAnchor(req),
	}
	if strings.TrimSpace(e.Kind) == "" {
		e.Kind = "Error"
	}
	if id, ok := a.userFrom(r.Context()); ok {
		e.UserID = &id
	}
	a.rec.Report(e)

	response.WriteSuccess(w, http.StatusAccepted, "v1", nil, map[string]bool{"received": true})
}

// pagePath keeps the path of a page and drops its query, which can carry
// anything.
func pagePath(page string) string {
	if i := strings.IndexAny(page, "?#"); i >= 0 {
		page = page[:i]
	}
	return page
}

// webAnchor is what groups a web error: Next's digest for a server error,
// else the top frame of the stack, else the page and message.
func webAnchor(req reportRequest) string {
	if d := strings.TrimSpace(req.Digest); d != "" {
		return "digest " + d
	}
	for _, line := range strings.Split(req.Stack, "\n") {
		line = strings.TrimSpace(line)
		// V8 frames start with "at "; Firefox and Safari use "fn@url".
		if strings.HasPrefix(line, "at ") || strings.Contains(line, "@") {
			return stripPosition(line)
		}
	}
	return pagePath(req.Page) + " " + strings.TrimSpace(req.Message)
}

// stripPosition drops a frame's :line:column, which shifts with every build,
// and keeps the function and file.
func stripPosition(frame string) string {
	frame = strings.TrimSuffix(frame, ")")
	for range 2 {
		i := strings.LastIndex(frame, ":")
		if i < 0 || !isDigits(frame[i+1:]) {
			break
		}
		frame = frame[:i]
	}
	return frame
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
