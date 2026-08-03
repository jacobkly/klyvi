// Package supabase is the server-side client for the two Supabase services
// the API brokers on behalf of clients: Storage (avatar objects) and the
// GoTrue admin API (auth-user deletion). It authenticates with the
// service-role key, which lives only in server env and bypasses RLS — the
// browser never sees it and never talks to these services directly.
package supabase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client calls the Supabase Storage and GoTrue admin REST APIs.
type Client struct {
	baseURL string // project URL, e.g. https://<ref>.supabase.co
	key     string // service-role key
	http    *http.Client
}

// New returns a client, or nil when url or key is empty — callers treat a
// nil client as "feature not configured" and degrade with a clear error
// instead of failing at startup, since most deployments only need these
// features once avatars ship.
func New(url, serviceRoleKey string) *Client {
	if url == "" || serviceRoleKey == "" {
		return nil
	}
	return &Client{
		baseURL: url,
		key:     serviceRoleKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Status: resp.StatusCode, Detail: fmt.Sprintf("%s %s: %s", method, path, snippet)}
	}
	return nil
}

// StatusError reports a non-2xx response from Supabase, carrying the status
// so callers can branch with errors.As instead of string matching.
type StatusError struct {
	Status int
	Detail string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("supabase: status %d: %s", e.Status, e.Detail)
}

// UploadPublicObject writes an object into a public bucket, overwriting any
// existing object at that path, and returns its public URL.
func (c *Client) UploadPublicObject(ctx context.Context, bucket, path, contentType string, data []byte) (string, error) {
	// x-upsert lets a re-upload replace the previous avatar in place, which
	// keeps the stored URL stable across changes.
	objectPath := fmt.Sprintf("/storage/v1/object/%s/%s", bucket, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+objectPath, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("build upload: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-upsert", "true")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", &StatusError{Status: resp.StatusCode, Detail: fmt.Sprintf("upload %s: %s", path, snippet)}
	}

	return fmt.Sprintf("%s/storage/v1/object/public/%s/%s", c.baseURL, bucket, path), nil
}

// DeleteObject removes an object. A 404 is treated as success — the caller
// wants the object gone, and it is.
func (c *Client) DeleteObject(ctx context.Context, bucket, path string) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/storage/v1/object/%s/%s", bucket, path), "", nil)
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// DeleteAuthUser removes the Supabase auth user via the GoTrue admin API.
// This is the final step of account deletion; the app-side rows are already
// gone by the time it is called.
func (c *Client) DeleteAuthUser(ctx context.Context, userID string) error {
	err := c.do(ctx, http.MethodDelete, "/auth/v1/admin/users/"+userID, "", nil)
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

func isStatus(err error, status int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}
