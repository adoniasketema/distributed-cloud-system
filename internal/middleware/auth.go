package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"nimbus/internal/auth"
)

type contextKey string

const UserIDKey contextKey = "user_id"

// AuthMiddleware creates a middleware that validates JWT tokens
func AuthMiddleware(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "missing authorization header", http.StatusUnauthorized)
				return
			}

			// Expect "Bearer <token>"
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				http.Error(w, "invalid authorization header format", http.StatusUnauthorized)
				return
			}

			tokenString := parts[1]

			// Parse into the same typed claims the issuer produced, so there is no
			// interface{} type assertion that can silently fail. WithValidMethods pins the
			// accepted algorithm before the key function runs, which is what rejects a token
			// re-presented as "none" or under an asymmetric algorithm.
			claims := &auth.Claims{}
			token, err := jwt.ParseWithClaims(
				tokenString,
				claims,
				func(token *jwt.Token) (interface{}, error) { return []byte(jwtSecret), nil },
				jwt.WithValidMethods([]string{auth.SigningMethod.Alg()}),
			)

			// Expiry is checked by the parser, so err covers expired tokens too.
			if err != nil || !token.Valid {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}

			userIDStr := claims.UserID()
			if userIDStr == "" {
				http.Error(w, "missing user id in token", http.StatusUnauthorized)
				return
			}

			// Embed userID in context
			ctx := context.WithValue(r.Context(), UserIDKey, userIDStr)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
