package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// jwksTestKey holds a freshly generated RSA key plus an httptest server that
// publishes the matching JWKS. Used to drive end-to-end signature verification
// in the middleware without depending on a real Supabase project.
type jwksTestKey struct {
	priv *rsa.PrivateKey
	kid  string
	srv  *httptest.Server
}

func newJWKSTestKey(t *testing.T) *jwksTestKey {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa generate: %v", err)
	}

	kid := "test-key"
	n := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes())

	jwks := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": kid,
			"n":   n,
			"e":   e,
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(srv.Close)

	return &jwksTestKey{priv: priv, kid: kid, srv: srv}
}

// sign produces a JWT signed with this test key's private half. claims are
// merged with sensible defaults; pass a future exp to make a valid token,
// a past one to make it expired.
func (k *jwksTestKey) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = k.kid
	signed, err := tok.SignedString(k.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// sentinelHandler returns 200 with the user UUID from context written into
// the response body, or 500 if the middleware did not put one there.
func sentinelHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := UserUUIDFromContext(r.Context())
		if !ok {
			http.Error(w, "no uuid in context", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(id.String()))
	})
}

func TestAuthMiddleware_RejectsMissingHeader(t *testing.T) {
	key := newJWKSTestKey(t)
	mw, err := NewAuthMiddleware(AuthConfig{JWKSURL: key.srv.URL})
	if err != nil {
		t.Fatalf("new mw: %v", err)
	}

	rec := httptest.NewRecorder()
	mw(sentinelHandler(t)).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthMiddleware_RejectsMalformedHeader(t *testing.T) {
	key := newJWKSTestKey(t)
	mw, err := NewAuthMiddleware(AuthConfig{JWKSURL: key.srv.URL})
	if err != nil {
		t.Fatalf("new mw: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic notatoken")
	rec := httptest.NewRecorder()
	mw(sentinelHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for non-Bearer header, got %d", rec.Code)
	}
}

func TestAuthMiddleware_RejectsBadSignature(t *testing.T) {
	key := newJWKSTestKey(t)
	mw, err := NewAuthMiddleware(AuthConfig{JWKSURL: key.srv.URL})
	if err != nil {
		t.Fatalf("new mw: %v", err)
	}

	// Sign with a *different* key — the JWKS will not match.
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = key.kid
	signed, _ := tok.SignedString(otherKey)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()
	mw(sentinelHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong-key signature, got %d", rec.Code)
	}
}

func TestAuthMiddleware_RejectsExpiredToken(t *testing.T) {
	key := newJWKSTestKey(t)
	mw, err := NewAuthMiddleware(AuthConfig{JWKSURL: key.srv.URL})
	if err != nil {
		t.Fatalf("new mw: %v", err)
	}

	signed := key.sign(t, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(-time.Hour).Unix(), // expired an hour ago
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()
	mw(sentinelHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", rec.Code)
	}
}

func TestAuthMiddleware_RejectsInvalidSubject(t *testing.T) {
	key := newJWKSTestKey(t)
	mw, err := NewAuthMiddleware(AuthConfig{JWKSURL: key.srv.URL})
	if err != nil {
		t.Fatalf("new mw: %v", err)
	}

	signed := key.sign(t, jwt.MapClaims{
		"sub": "not-a-uuid",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()
	mw(sentinelHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for non-UUID sub claim, got %d", rec.Code)
	}
}

func TestAuthMiddleware_AcceptsValidToken(t *testing.T) {
	key := newJWKSTestKey(t)
	mw, err := NewAuthMiddleware(AuthConfig{JWKSURL: key.srv.URL})
	if err != nil {
		t.Fatalf("new mw: %v", err)
	}

	userID := uuid.New()
	signed := key.sign(t, jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()
	mw(sentinelHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid token, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != userID.String() {
		t.Errorf("expected handler to see uuid %s, got %q", userID, got)
	}
}

func TestAuthMiddleware_EnforcesIssuerWhenSet(t *testing.T) {
	key := newJWKSTestKey(t)
	mw, err := NewAuthMiddleware(AuthConfig{JWKSURL: key.srv.URL, Issuer: "https://expected.example.com"})
	if err != nil {
		t.Fatalf("new mw: %v", err)
	}

	signed := key.sign(t, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
		"iss": "https://wrong-issuer.example.com",
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()
	mw(sentinelHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong issuer, got %d", rec.Code)
	}
}
