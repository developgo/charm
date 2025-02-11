package server

import (
	"context"
	"crypto"
	"fmt"
	"log"
	"net/http"
	"strings"

	jwtmiddleware "github.com/auth0/go-jwt-middleware/v2"
	"github.com/auth0/go-jwt-middleware/v2/validator"
	charm "github.com/charmbracelet/charm/proto"
)

type contextKey string

var ctxUserKey contextKey = "charmUser"

// RequestLimitMiddleware limits the request body size to the specified limit.
func RequestLimitMiddleware() func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var maxRequestSize int64
			if strings.HasPrefix(r.URL.Path, "/v1/fs") {
				maxRequestSize = 1 << 30 // limit request size to 1GB for fs endpoints
			} else {
				maxRequestSize = 1 << 20 // limit request size to 1MB for other endpoints
			}
			// Check if the request body is too large using Content-Length
			if r.ContentLength > maxRequestSize {
				http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			}
			// Limit body read using MaxBytesReader
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)
			h.ServeHTTP(w, r)
		})
	}
}

// PublicPrefixesMiddleware allows for the specification of non-authed URL
// prefixes. These won't be checked for JWT bearers or Charm user accounts.
func PublicPrefixesMiddleware(prefixes []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			public := false
			for _, p := range prefixes {
				if strings.HasPrefix(r.URL.Path, p) {
					public = true
				}
			}
			ctx := context.WithValue(r.Context(), "public", public)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// JWTMiddleware creates a new middleware function that will validate JWT
// tokens based on the supplied public key.
func JWTMiddleware(pk crypto.PublicKey, iss string, aud []string) (func(http.Handler) http.Handler, error) {
	jm, err := jwtMiddlewareImpl(pk, iss, aud)
	if err != nil {
		return nil, err
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r) {
				next.ServeHTTP(w, r)
			} else {
				jm(next).ServeHTTP(w, r)
			}
		})
	}, nil
}

// CharmUserMiddleware looks up and authenticates a Charm user based on the
// provided JWT in the request.
func CharmUserMiddleware(s *HTTPServer) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r) {
				h.ServeHTTP(w, r)
			} else {
				id, err := charmIDFromRequest(r)
				if err != nil {
					log.Printf("cannot get charm id from request: %s", err)
					s.renderError(w)
					return
				}
				u, err := s.db.GetUserWithID(id)
				if err == charm.ErrMissingUser {
					s.renderCustomError(w, fmt.Sprintf("missing user for id '%s'", id), http.StatusNotFound)
					return
				} else if err != nil {
					log.Printf("cannot read request body: %s", err)
					s.renderError(w)
					return
				}
				ctx := context.WithValue(r.Context(), ctxUserKey, u)
				h.ServeHTTP(w, r.WithContext(ctx))
			}
		})
	}
}

func isPublic(r *http.Request) bool {
	return r.Context().Value("public") == true
}

func charmIDFromRequest(r *http.Request) (string, error) {
	claims := r.Context().Value(jwtmiddleware.ContextKey{})
	if claims == "" {
		return "", fmt.Errorf("missing jwt claims key in context")
	}
	cl := claims.(*validator.ValidatedClaims).RegisteredClaims
	sub := cl.Subject
	if sub == "" {
		return "", fmt.Errorf("missing subject key in claims map")
	}
	return sub, nil
}

func jwtMiddlewareImpl(pk crypto.PublicKey, iss string, aud []string) (func(http.Handler) http.Handler, error) {
	kf := func(ctx context.Context) (interface{}, error) {
		return pk, nil
	}
	v, err := validator.New(
		kf,
		validator.EdDSA,
		iss,
		aud,
	)
	if err != nil {
		return nil, err
	}
	mw := jwtmiddleware.New(v.ValidateToken)
	return mw.CheckJWT, nil
}
