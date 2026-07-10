// Command session-auth demonstrates Principal attachment via signed session cookies.
// It uses stdlib crypto/hmac for signatures (no external auth library).
//
// WARNING: This is a demo. The session store is in-memory; production apps must use
// their identity provider and persistent session storage. This example shows the
// pgdesk integration pattern, not a secure auth system.
//
//	docker run --rm -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:16
//	SECRET_KEY=dev-secret-change-in-production \
//	  go run ./examples/session-auth
//
// Then open http://localhost:8080/admin/
// Log in as user "alice" with password "password" (hardcoded demo only).
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk"
)

func main() {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	// Create a small table with a few rows so there's something to see.
	if _, err := pool.Exec(ctx, schema); err != nil {
		log.Fatal(err)
	}

	secretKey := os.Getenv("SECRET_KEY")
	if secretKey == "" {
		secretKey = "dev-secret-change-in-production"
	}

	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte(secretKey)),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(sessionMiddleware(secretKey)),
		pgdesk.WithResource("users", func(r *pgdesk.Resource) {
			r.ListDisplay("id", "email", "active", "created_at")
			r.SearchFields("email")
			r.Filters("active")
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer admin.Close()

	mux := http.NewServeMux()

	// Login page and handler (demo only).
	mux.HandleFunc("/login", loginPage)
	mux.HandleFunc("/login/do", loginHandler(secretKey))
	mux.HandleFunc("/logout", logoutHandler)

	// Protect the admin with middleware that checks the session cookie.
	admin.Mount(mux)

	log.Println("Demo login: http://localhost:8080/login")
	log.Println("Admin: http://localhost:8080/admin/ (requires login)")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// sessionMiddleware checks the session cookie, validates its signature, and
// attaches the Principal to the request context.
func sessionMiddleware(secretKey string) pgdesk.Middleware {
	key := []byte(secretKey)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("session")
			if err == nil {
				if op, err := decodeSession(cookie.Value, key); err == nil {
					// Signature is valid; attach the operator and serve.
					r = r.WithContext(pgdesk.WithPrincipal(r.Context(), op))
					next.ServeHTTP(w, r)
					return
				}
			}
			// No valid session. Redirect to login.
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		})
	}
}

// loginPage serves the demo login form.
func loginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Login - pgdesk Demo</title></head>
<body>
<h1>pgdesk Session Auth Demo</h1>
<p>This is a demo login page. In production, use your identity provider.</p>
<form method="POST" action="/login/do">
	<label>Username: <input type="text" name="username" value="alice" required></label><br>
	<label>Password: <input type="password" name="password" value="password" required></label><br>
	<button type="submit">Login</button>
</form>
<p><em>Hardcoded: alice / password</em></p>
</body>
</html>`)
}

// loginHandler validates credentials and sets a signed session cookie.
func loginHandler(secretKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := r.FormValue("username")
		password := r.FormValue("password")

		// Demo: hardcoded credentials. Production: query a user DB, validate
		// against bcrypt/scrypt/argon2, and get the user ID from there.
		if username != "alice" || password != "password" {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}

		// Encode and sign the session.
		op := demoOperator{ID: username, Name: "Alice"}
		value, err := encodeSession(op, []byte(secretKey))
		if err != nil {
			log.Printf("session encode failed: %v", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}

		// Set the cookie (HttpOnly, no Secure in dev, 24h expiry).
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    value,
			Path:     "/",
			MaxAge:   24 * 60 * 60,
			HttpOnly: true,
			// Secure: true, // Enable in production with HTTPS
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	}
}

// logoutHandler clears the session cookie.
func logoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// demoOperator is a concrete Principal implementation for this demo.
type demoOperator struct {
	ID   string // user ID
	Name string // display name
}

func (o demoOperator) SubjectID() string   { return o.ID }
func (o demoOperator) DisplayName() string { return o.Name }

// encodeSession signs and returns a session cookie value. Format:
// "operator:user_id:display_name:timestamp:hmac_signature"
// This is a demo; production code would use a proper session framework.
func encodeSession(op demoOperator, secretKey []byte) (string, error) {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	payload := fmt.Sprintf("%s:%s:%s", op.ID, op.Name, timestamp)

	// Sign the payload.
	h := hmac.New(sha256.New, secretKey)
	h.Write([]byte(payload))
	sig := hex.EncodeToString(h.Sum(nil))

	return fmt.Sprintf("%s:%s", payload, sig), nil
}

// decodeSession validates the signature and decodes a session cookie.
func decodeSession(cookie string, secretKey []byte) (demoOperator, error) {
	parts := strings.Split(cookie, ":")
	if len(parts) != 4 {
		return demoOperator{}, errors.New("invalid session format")
	}

	id := parts[0]
	name := parts[1]
	timestamp := parts[2]
	sig := parts[3]

	// Re-sign and check.
	payload := fmt.Sprintf("%s:%s:%s", id, name, timestamp)
	h := hmac.New(sha256.New, secretKey)
	h.Write([]byte(payload))
	expectedSig := hex.EncodeToString(h.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return demoOperator{}, errors.New("signature mismatch")
	}

	// Optionally check timestamp (e.g., reject if older than 24h).
	// For demo, skip expiry check.

	return demoOperator{ID: id, Name: name}, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      text        NOT NULL UNIQUE,
    active     boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO users (email, active) VALUES
    ('alice@example.com', true),
    ('bob@example.com', true),
    ('charlie@example.com', false)
ON CONFLICT (email) DO NOTHING;
`
