package users_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/http/middleware"
	"klyvi-api/internal/users"
)

// TestE2E_ProtectedRouteChain verifies the full chain that a real frontend
// request goes through on a protected route:
//
//   1. JWT auth middleware verifies a signed token against a JWKS endpoint.
//   2. The middleware lifts the `sub` claim into request context.
//   3. EnsureUserMiddleware upserts a row in the `users` table.
//   4. The protected handler reads the UUID from context and looks up the row.
//
// Unit tests cover each of these in isolation; this test is the proof that
// they actually compose without a missing link. Step 1 uses a mock JWKS
// (a real Supabase JWT requires running the server end-to-end with a real
// frontend, which is out of scope for the automated suite).
func TestE2E_ProtectedRouteChain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}

	root := findRepoRoot(t)
	if err := godotenv.Load(filepath.Join(root, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}

	dbCfg := config.NewDB()
	dbConn, err := db.New(*dbCfg)
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	// Registered as a cleanup rather than deferred: t.Cleanup callbacks run
	// after the function returns, so a deferred Close would shut the pool
	// before the row cleanup below could use it. LIFO ordering means this
	// close runs last.
	t.Cleanup(func() { dbConn.Close() })

	// --- mock JWKS + token signing
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa generate: %v", err)
	}
	const kid = "e2e-test-key"
	jwks := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
		}},
	}
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksSrv.Close()

	authMW, err := middleware.NewAuthMiddleware(middleware.AuthConfig{JWKSURL: jwksSrv.URL})
	if err != nil {
		t.Fatalf("auth mw: %v", err)
	}

	// --- real users service against the real DB
	repo := users.NewRepository(dbConn)
	svc := users.NewService(repo)
	api := users.NewAPI(svc)

	userID := uuid.New()
	t.Cleanup(func() {
		_, _ = dbConn.Exec(`delete from users where id = $1`, userID)
	})

	// --- assemble the protected route chain that production uses
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authMW)
		r.Use(svc.EnsureUserMiddleware())
		r.Get("/users/me", api.GetMe)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	// --- 1. unauthenticated → 401
	resp, err := http.Get(srv.URL + "/users/me")
	if err != nil {
		t.Fatalf("anon GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anon: expected 401, got %d", resp.StatusCode)
	}

	// --- 2. valid JWT → 200 + auto-created user row + handler sees UUID
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed: expected 200, got %d body=%s", resp.StatusCode, body)
	}

	// Response is the standard envelope; assert the user row is in there
	// keyed by our UUID — proves the EnsureUser+GetMe chain worked.
	var envelope struct {
		Data users.User `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v body=%s", err, body)
	}
	if envelope.Data.ID != userID {
		t.Errorf("expected user id %s in response, got %s", userID, envelope.Data.ID)
	}
	if envelope.Data.Username == "" {
		t.Error("expected default username on auto-created row, got empty")
	}
}

// TestE2E_UsernameConflictStatus proves the whole stack — JWT, handler,
// service, repository, real unique index — turns a duplicate username into a
// 409 with a clean message.
//
// The layers are each covered on their own: the handler maps typed errors to
// status codes, the repository translates SQLSTATE 23505. This asserts they
// actually compose, because the web client is about to drop its regex match
// on the Postgres error text and branch on this status instead.
func TestE2E_UsernameConflictStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}

	root := findRepoRoot(t)
	if err := godotenv.Load(filepath.Join(root, ".env.dev")); err != nil {
		t.Skipf("could not load .env.dev: %v", err)
	}

	dbCfg := config.NewDB()
	dbConn, err := db.New(*dbCfg)
	if err != nil {
		t.Skipf("DB unreachable: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa generate: %v", err)
	}
	const kid = "e2e-conflict-key"
	jwks := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
		}},
	}
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksSrv.Close()

	authMW, err := middleware.NewAuthMiddleware(middleware.AuthConfig{JWKSURL: jwksSrv.URL})
	if err != nil {
		t.Fatalf("auth mw: %v", err)
	}

	repo := users.NewRepository(dbConn)
	svc := users.NewService(repo)
	api := users.NewAPI(svc)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authMW)
		r.Use(svc.EnsureUserMiddleware())
		r.Patch("/users/me", api.UpdateMe)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	tokenFor := func(id uuid.UUID) string {
		t.Helper()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"sub": id.String(),
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = kid
		signed, err := tok.SignedString(priv)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return signed
	}

	patch := func(id uuid.UUID, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/users/me", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenFor(id))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("patch: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	holder, claimer := uuid.New(), uuid.New()
	t.Cleanup(func() {
		for _, id := range []uuid.UUID{holder, claimer} {
			if _, err := dbConn.Exec(`delete from users where id = $1`, id); err != nil {
				t.Errorf("cleanup %s: %v", id, err)
			}
		}
	})

	name := "e2e_" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")

	// The first user claims the name.
	if code, body := patch(holder, `{"username":"`+name+`"}`); code != http.StatusOK {
		t.Fatalf("first claim: got %d, want 200 (%s)", code, body)
	}

	// A second user asking for the same name — differing only by case, so
	// this also exercises the lower() index — is a conflict, not a 400 and
	// not a leaked driver error.
	code, body := patch(claimer, `{"username":"`+strings.ToUpper(name)+`"}`)
	if code != http.StatusConflict {
		t.Fatalf("duplicate claim: got %d, want 409 (%s)", code, body)
	}

	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &errBody); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if errBody.Error != users.ErrUsernameTaken.Error() {
		t.Errorf("error = %q, want %q", errBody.Error, users.ErrUsernameTaken.Error())
	}
	for _, leak := range []string{"SQLSTATE", "23505", "duplicate key"} {
		if strings.Contains(errBody.Error, leak) {
			t.Errorf("response leaks %q: %s", leak, errBody.Error)
		}
	}

	// A bad format on the same route still reports 400, so the 409 above is
	// a real distinction rather than everything collapsing to one code.
	if code, body := patch(claimer, `{"username":"no spaces allowed"}`); code != http.StatusBadRequest {
		t.Errorf("invalid format: got %d, want 400 (%s)", code, body)
	}
}
