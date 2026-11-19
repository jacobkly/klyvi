package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/response"
)

// AuthConfig configures the JWT verification middleware. JWKSURL is required;
// Issuer and Audience are optional strict-claim checks.
type AuthConfig struct {
	JWKSURL  string
	Issuer   string
	Audience string
}

// Authenticator verifies Supabase-style JWTs against the project's JWKS. The
// JWKS is loaded once at startup; the keyfunc library handles refresh on its
// own schedule.
//
// Tokens must use an asymmetric signing algorithm (RS256/ES256); HS256 is
// explicitly rejected to prevent the classic "alg: none" / shared-secret
// confusion class of attack.
type Authenticator struct {
	jwks   keyfunc.Keyfunc
	parser *jwt.Parser
}

// NewAuthMiddleware builds the required-auth middleware: a request without a
// valid token gets a 401.
func NewAuthMiddleware(cfg AuthConfig) (func(http.Handler) http.Handler, error) {
	a, err := NewAuthenticator(cfg)
	if err != nil {
		return nil, err
	}
	return a.Required, nil
}

// NewAuthenticator loads the JWKS and builds the token parser.
func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	if cfg.JWKSURL == "" {
		return nil, errors.New("JWKSURL is required")
	}

	jwks, err := keyfunc.NewDefaultCtx(context.Background(), []string{cfg.JWKSURL})
	if err != nil {
		return nil, err
	}

	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30 * time.Second),
	}
	if cfg.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(cfg.Issuer))
	}
	if cfg.Audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(cfg.Audience))
	}
	return &Authenticator{jwks: jwks, parser: jwt.NewParser(parserOpts...)}, nil
}

var (
	errNoToken      = errors.New("missing or malformed authorization header")
	errInvalidToken = errors.New("invalid token")
	errClaims       = errors.New("invalid claims")
	errSubject      = errors.New("invalid subject claim")
)

// userFrom verifies the request's bearer token and returns its subject.
func (a *Authenticator) userFrom(r *http.Request) (uuid.UUID, error) {
	tok := extractBearer(r)
	if tok == "" {
		return uuid.Nil, errNoToken
	}
	parsed, err := a.parser.Parse(tok, a.jwks.Keyfunc)
	if err != nil || !parsed.Valid {
		return uuid.Nil, errInvalidToken
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, errClaims
	}
	sub, _ := claims["sub"].(string)
	id, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, errSubject
	}
	return id, nil
}

// Required lets a request through only with a valid token, and puts its
// user in the context.
func (a *Authenticator) Required(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.userFrom(r)
		if err != nil {
			response.WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUserUUID(r.Context(), id)))
	})
}

// Optional lets every request through, and puts the user in the context
// when the token is valid. For public routes that use the user if there
// is one.
func (a *Authenticator) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, err := a.userFrom(r); err == nil {
			r = r.WithContext(WithUserUUID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}
