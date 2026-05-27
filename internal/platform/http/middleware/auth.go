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

// NewAuthMiddleware builds a middleware that verifies Supabase-style JWTs
// against the project's JWKS. The JWKS is loaded once at startup; the keyfunc
// library handles refresh on its own schedule.
//
// Tokens must use an asymmetric signing algorithm (RS256/ES256); HS256 is
// explicitly rejected to prevent the classic "alg: none" / shared-secret
// confusion class of attack.
func NewAuthMiddleware(cfg AuthConfig) (func(http.Handler) http.Handler, error) {
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
	parser := jwt.NewParser(parserOpts...)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := extractBearer(r)
			if tok == "" {
				response.WriteError(w, http.StatusUnauthorized, "missing or malformed authorization header")
				return
			}

			parsed, err := parser.Parse(tok, jwks.Keyfunc)
			if err != nil || !parsed.Valid {
				response.WriteError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			claims, ok := parsed.Claims.(jwt.MapClaims)
			if !ok {
				response.WriteError(w, http.StatusUnauthorized, "invalid claims")
				return
			}
			sub, _ := claims["sub"].(string)
			id, err := uuid.Parse(sub)
			if err != nil {
				response.WriteError(w, http.StatusUnauthorized, "invalid subject claim")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithUserUUID(r.Context(), id)))
		})
	}, nil
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}
