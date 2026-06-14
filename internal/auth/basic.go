package auth

import (
	"encoding/base64"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// PasswordAuthMiddleware returns a middleware that validates the password header.
// The password header value is Base64-encoded on the client side.
// The server decodes it and compares against the expected hash using constant-time comparison.
func PasswordAuthMiddleware(expectedHash string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			password := r.Header.Get("password")
			if password == "" {
				http.Error(w, "Unauthorized: missing password", http.StatusUnauthorized)
				return
			}

			decoded, err := base64.StdEncoding.DecodeString(password)
			if err != nil {
				http.Error(w, "Unauthorized: invalid password encoding", http.StatusUnauthorized)
				return
			}

			err = bcrypt.CompareHashAndPassword([]byte(expectedHash), []byte(strings.TrimSpace(string(decoded))))
			if err != nil {
				http.Error(w, "Unauthorized: invalid password", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ValidatePassword checks if the provided password matches the expected hash.
//
// R10-PASSWORD-TRIM: this function does NOT trim the password. Trimming a
// user's password silently changes the input they intended to submit and
// reduces effective entropy. The setup wizard that creates the hash also
// does not trim the password, so trimming here would only ever matter if a
// caller introduced a new trimming path. Bcrypt itself is byte-for-byte
// compare, so trailing whitespace in the password is significant.
//
// Callers (e.g. the login handler) trim the username (not the password) to
// tolerate accidental leading/trailing spaces in the user identifier.
func ValidatePassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
