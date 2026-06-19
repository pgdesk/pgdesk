// Command basic is a runnable example wiring pgdesk into a standard-library
// http.Server with production-appropriate timeouts (O2), graceful shutdown and
// SIGHUP-driven catalog reload (O1/D1/O3), and a readiness probe (O3).
//
// It expects PGDESK_DSN to point at a database containing the demo schema in
// examples/basic/schema.sql. Run:
//
//	psql "$PGDESK_DSN" -f examples/basic/schema.sql
//	PGDESK_DSN=postgres://... go run ./examples/basic
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dsn := os.Getenv("PGDESK_DSN")
	if dsn == "" {
		return errors.New("set PGDESK_DSN to a PostgreSQL connection string")
	}
	secret := []byte(os.Getenv("PGDESK_SECRET"))
	if len(secret) == 0 {
		// Demo only. In production load this from a secret manager (>= 32 bytes).
		secret = []byte("dev-only-insecure-key-change-me-please")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The host owns the pool's lifecycle; pgdesk never closes it (O3).
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	admin, err := pgdesk.New(pool,
		pgdesk.WithTitle("Operations Admin"),
		pgdesk.WithBasePath("/admin"),
		pgdesk.WithSchemas("public"),
		pgdesk.WithSecretKey(secret),
		pgdesk.WithLogger(logger),
		pgdesk.WithQueryTimeout(15*time.Second),
		pgdesk.WithAuthorizer(pgdesk.AllowAll{}), // demo: any authenticated operator is allowed
		pgdesk.WithLoginURL("/login"),
		pgdesk.WithMiddleware(demoAuth),                 // injects a Principal; real hosts use their auth
		pgdesk.WithTxAuditLogger(auditLogger{}),         // durable, in-transaction audit (O4)
		pgdesk.WithMetrics(slogMetrics{logger: logger}), // bridge internals to your metrics (O5)
	)
	if err != nil {
		return err
	}
	defer admin.Close()

	admin.Resource("users", func(r *pgdesk.Resource) {
		r.Label = "User"
		r.LabelPlural = "Users"
		r.ListDisplay("id", "email", "status", "created_at")
		r.SearchFields("email", "full_name")
		r.Filters("status", "created_at")
		r.Readonly("id", "created_at", "updated_at")
		r.DefaultSort("-created_at")

		// A bulk action: suspend the selected users, transactionally and audited.
		r.Action("suspend", "Suspend selected", suspendUsers,
			pgdesk.WithConfirm("Suspend the selected users?"))
	})

	mux := http.NewServeMux()
	admin.Mount(mux)

	// Readiness probe wired by the host — pgdesk registers no magic route (O3).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, req *http.Request) {
		if err := admin.Healthy(req.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// SIGHUP → hot-swap the catalog (D1). Kept live-on-failure by Reload itself.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := admin.Reload(context.Background()); err != nil {
				logger.Error("catalog reload failed", "error", err)
			}
		}
	}()

	go func() {
		logger.Info("pgdesk example listening", "addr", srv.Addr, "admin", "/admin/")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// suspendUsers is a bulk action: it sets status='suspended' for the selected
// rows using a single parameterized statement inside the mutation's transaction
// (O4). keys holds one []any per selected row; here each row's key is a single
// bigint id.
func suspendUsers(ctx context.Context, tx pgx.Tx, keys [][]any) (string, error) {
	ids := make([]any, 0, len(keys))
	for _, k := range keys {
		if len(k) == 1 {
			ids = append(ids, k[0])
		}
	}
	tag, err := tx.Exec(ctx, "UPDATE users SET status='suspended', updated_at=now() WHERE id = ANY($1)", ids)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Suspended %d user(s).", tag.RowsAffected()), nil
}

// auditLogger is a durable, transactional audit sink (O4). Because it writes
// using the mutation's own tx, a mutation cannot commit without its audit row,
// and an audit failure rolls the mutation back.
type auditLogger struct{}

func (auditLogger) LogAuditTx(ctx context.Context, tx pgx.Tx, e pgdesk.AuditEvent) error {
	before, _ := json.Marshal(e.Before)
	after, _ := json.Marshal(e.After)
	_, err := tx.Exec(ctx,
		`INSERT INTO audit_log
		   (actor_id, action, resource, action_name, row_key, before_snap, after_snap, source_ip, request_id, at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		e.ActorID, string(e.Action), e.Resource, e.ActionName, e.Key,
		nullableJSON(before, e.Before), nullableJSON(after, e.After),
		e.SourceIP, e.RequestID, e.At)
	return err
}

func nullableJSON(b []byte, src map[string]any) any {
	if src == nil {
		return nil
	}
	return b
}

// slogMetrics bridges pgdesk's Metrics hook to structured logs (O5). A real host
// would increment Prometheus/OTel counters and histograms here.
type slogMetrics struct{ logger *slog.Logger }

func (m slogMetrics) ObserveRequest(route string, status int, dur time.Duration) {
	m.logger.Debug("admin.request", "route", route, "status", status, "dur", dur)
}

func (m slogMetrics) ObserveQuery(op string, dur time.Duration, err error) {
	m.logger.Debug("admin.query", "op", op, "dur", dur, "err", err)
}

// demoPrincipal is a stand-in operator identity. Real hosts implement
// pgdesk.Principal on their own user type.
type demoPrincipal struct{}

func (demoPrincipal) SubjectID() string   { return "demo-operator" }
func (demoPrincipal) DisplayName() string { return "Demo Operator" }

// demoAuth injects a fixed Principal so the example is usable without an auth
// system. NEVER do this in production — authenticate the request first.
func demoAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := pgdesk.WithPrincipal(r.Context(), demoPrincipal{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
