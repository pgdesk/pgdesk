//go:build integration

package pgdesk_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk"
)

const relatedSchema = `
DROP TABLE IF EXISTS rel_turns; DROP TABLE IF EXISTS rel_noindex; DROP TABLE IF EXISTS rel_msgs;
DROP TABLE IF EXISTS rel_sessions; DROP TABLE IF EXISTS rel_people;
CREATE TABLE rel_sessions (id uuid PRIMARY KEY, name text NOT NULL, tenant text NOT NULL DEFAULT 'acme');
CREATE TABLE rel_turns (
    session_id uuid NOT NULL REFERENCES rel_sessions(id),
    position   int  NOT NULL,
    question   text NOT NULL,
    answer     text NOT NULL,
    secret     text NOT NULL DEFAULT 'hush',
    tenant     text NOT NULL DEFAULT 'acme',
    PRIMARY KEY (session_id, position)
);
CREATE TABLE rel_noindex (id bigint PRIMARY KEY, session_id uuid NOT NULL REFERENCES rel_sessions(id));
CREATE TABLE rel_people (id bigint PRIMARY KEY, name text NOT NULL);
CREATE TABLE rel_msgs (id bigint PRIMARY KEY, sender_id bigint NOT NULL REFERENCES rel_people(id), receiver_id bigint NOT NULL REFERENCES rel_people(id), body text NOT NULL);
INSERT INTO rel_sessions VALUES ('11111111-1111-1111-1111-111111111111', 'Ada interview'), ('22222222-2222-2222-2222-222222222222', 'Empty interview');
INSERT INTO rel_turns (session_id, position, question, answer, tenant) VALUES
    ('11111111-1111-1111-1111-111111111111', 2, 'Third?', 'third answer', 'acme'),
    ('11111111-1111-1111-1111-111111111111', 0, 'First?', 'first answer', 'acme'),
    ('11111111-1111-1111-1111-111111111111', 1, 'Second?', repeat('long answer ', 60), 'other');
INSERT INTO rel_people VALUES (1, 'Ada'), (2, 'Grace');
INSERT INTO rel_msgs VALUES (1, 1, 2, 'hello');
`

const adaSession = "11111111-1111-1111-1111-111111111111"

type relatedConfig struct {
	opts    []pgdesk.Option
	related []pgdesk.RelatedOption
	turns   func(*pgdesk.Resource)
}

func relatedAdmin(t *testing.T, c relatedConfig) (*pgdesk.Admin, error) {
	t.Helper()
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), relatedSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	admin, err := pgdesk.New(pool, append([]pgdesk.Option{
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("rel_sessions", func(r *pgdesk.Resource) {
			r.LabelColumn("name")
			r.Related("rel_turns", c.related...)
		}),
		pgdesk.WithResource("rel_turns", func(r *pgdesk.Resource) {
			r.Hidden("secret")
			r.DefaultSort("-position")
			r.Filters("session_id")
			if c.turns != nil {
				c.turns(r)
			}
		}),
	}, c.opts...)...)
	if err == nil {
		t.Cleanup(func() { _ = admin.Close() })
	}
	return admin, err
}

func detail(admin *pgdesk.Admin, id string) *httptest.ResponseRecorder {
	return do(admin, httptest.NewRequest("GET", "/admin/rel_sessions/"+id, nil))
}

func TestARelatedSectionShowsTheChildRowsInTheAskedOrderWithoutHiddenColumns(t *testing.T) {
	admin, err := relatedAdmin(t, relatedConfig{related: []pgdesk.RelatedOption{pgdesk.RelatedSort("position"), pgdesk.RelatedTitle("Conversation")}})
	if err != nil {
		t.Fatal(err)
	}
	rec := detail(admin, adaSession)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Conversation") {
		t.Fatalf("%d: no section\n%s", rec.Code, body)
	}
	first, second, third := strings.Index(body, "First?"), strings.Index(body, "Second?"), strings.Index(body, "Third?")
	if first < 0 || !(first < second && second < third) {
		t.Errorf("rows are not in position order: %d %d %d", first, second, third)
	}
	if strings.Contains(body, "hush") || strings.Contains(body, ">Secret<") {
		t.Error("a column the child hides is shown")
	}
	if !strings.Contains(body, strings.Repeat("long answer ", 60)) {
		t.Error("a long text value is cut")
	}
	if strings.Contains(body, ">Session<") {
		t.Error("the column that points back is repeated on every row")
	}
	if !strings.Contains(body, `href="/admin/rel_turns/`) {
		t.Error("a row does not link to its own page")
	}
}

func TestWithoutASortTheChildsDefaultOrderIsUsed(t *testing.T) {
	admin, err := relatedAdmin(t, relatedConfig{})
	if err != nil {
		t.Fatal(err)
	}
	body := detail(admin, adaSession).Body.String()
	if first, third := strings.Index(body, "First?"), strings.Index(body, "Third?"); !(third >= 0 && third < first) {
		t.Errorf("the child's newest-first default was not used: %d %d", first, third)
	}
}

func TestAnEmptySectionSaysSo(t *testing.T) {
	admin, err := relatedAdmin(t, relatedConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if body := detail(admin, "22222222-2222-2222-2222-222222222222").Body.String(); !strings.Contains(body, "None.") {
		t.Error("an empty section is not marked")
	}
}

func TestMoreRowsThanTheLimitAreCutAndTheListIsOffered(t *testing.T) {
	admin, err := relatedAdmin(t, relatedConfig{related: []pgdesk.RelatedOption{pgdesk.RelatedLimit(2), pgdesk.RelatedSort("position")}})
	if err != nil {
		t.Fatal(err)
	}
	body := detail(admin, adaSession).Body.String()
	if strings.Contains(body, "Third?") || !strings.Contains(body, "Showing the first 2.") {
		t.Errorf("the limit was not applied\n%s", body)
	}
	if !strings.Contains(body, "/admin/rel_turns?f_session_id="+adaSession) {
		t.Error("no link to the full list")
	}
}

func TestNoLinkToTheFullListWhenTheChildCannotFilterByTheReference(t *testing.T) {
	admin, err := relatedAdmin(t, relatedConfig{
		related: []pgdesk.RelatedOption{pgdesk.RelatedLimit(1)},
		turns:   func(r *pgdesk.Resource) { r.Filters("tenant") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if body := detail(admin, adaSession).Body.String(); !strings.Contains(body, "Showing the first 1.") || strings.Contains(body, "Show all") {
		t.Error("a link was offered that the child's list cannot honour")
	}
}

func TestASectionTheViewerMayNotListIsLeftOut(t *testing.T) {
	deny := pgdesk.AuthorizerFunc(func(_ context.Context, a pgdesk.Attributes) (pgdesk.Decision, error) {
		if a.Capability == pgdesk.CapList && a.Resource == "rel_turns" {
			return pgdesk.Deny, nil
		}
		return pgdesk.Allow, nil
	})
	admin, err := relatedAdmin(t, relatedConfig{opts: []pgdesk.Option{pgdesk.WithAuthorizer(deny)}})
	if err != nil {
		t.Fatal(err)
	}
	rec := detail(admin, adaSession)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "First?") || strings.Contains(rec.Body.String(), "pg-related") {
		t.Errorf("%d: a section the viewer cannot list is shown", rec.Code)
	}
}

func TestTheChildsScopeLimitsTheRowsOfASection(t *testing.T) {
	scope := pgdesk.ScopeOnly(func(_ context.Context, a pgdesk.Attributes) ([]pgdesk.Constraint, error) {
		if a.Resource == "rel_turns" {
			return []pgdesk.Constraint{pgdesk.Eq("tenant", "acme")}, nil
		}
		return nil, nil
	})
	admin, err := relatedAdmin(t, relatedConfig{opts: []pgdesk.Option{pgdesk.WithAuthorizer(pgdesk.DenyOverrides(pgdesk.AllowAll, scope))}})
	if err != nil {
		t.Fatal(err)
	}
	body := detail(admin, adaSession).Body.String()
	if !strings.Contains(body, "First?") || strings.Contains(body, "Second?") {
		t.Errorf("the scope did not apply to the section\n%s", body)
	}
}

func TestAMistakeInARelatedStopsTheStart(t *testing.T) {
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), relatedSchema); err != nil {
		t.Fatal(err)
	}
	try := func(name string, parent string, configure func(*pgdesk.Resource), want string) {
		t.Run(name, func(t *testing.T) {
			resource := func(table string) pgdesk.Option {
				if table == parent {
					return pgdesk.WithResource(table, configure)
				}
				return pgdesk.WithResource(table, nil)
			}
			_, err := pgdesk.New(pool,
				pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
				pgdesk.WithAuthorizer(pgdesk.AllowAll),
				resource("rel_sessions"), resource("rel_turns"), resource("rel_msgs"), resource("rel_people"),
			)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("got %v, want it to mention %q", err, want)
			}
		})
	}
	try("an unregistered child", "rel_sessions", func(r *pgdesk.Resource) { r.Related("nope") }, "register it")
	try("no foreign key", "rel_people", func(r *pgdesk.Resource) { r.Related("rel_turns") }, "no single-column foreign key")
	try("two foreign keys", "rel_people", func(r *pgdesk.Resource) { r.Related("rel_msgs") }, "RelatedVia")
	try("a column that is not a reference", "rel_people", func(r *pgdesk.Resource) { r.Related("rel_msgs", pgdesk.RelatedVia("body")) }, "no single-column foreign key \"body\"")
	try("an unknown column", "rel_sessions", func(r *pgdesk.Resource) { r.Related("rel_turns", pgdesk.RelatedColumns("nope")) }, "no column \"nope\"")
	try("an unknown sort", "rel_sessions", func(r *pgdesk.Resource) { r.Related("rel_turns", pgdesk.RelatedSort("-nope")) }, "no column \"nope\"")
}

func TestRelatedViaChoosesBetweenSeveralForeignKeys(t *testing.T) {
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), relatedSchema); err != nil {
		t.Fatal(err)
	}
	admin, err := pgdesk.New(pool,
		pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
		pgdesk.WithAuthorizer(pgdesk.AllowAll),
		pgdesk.WithMiddleware(withPrincipal),
		pgdesk.WithResource("rel_people", func(r *pgdesk.Resource) {
			r.LabelColumn("name")
			r.Related("rel_msgs", pgdesk.RelatedVia("receiver_id"), pgdesk.RelatedTitle("Received"))
		}),
		pgdesk.WithResource("rel_msgs", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	grace := do(admin, httptest.NewRequest("GET", "/admin/rel_people/2", nil)).Body.String()
	ada := do(admin, httptest.NewRequest("GET", "/admin/rel_people/1", nil)).Body.String()
	if !strings.Contains(grace, "hello") || strings.Contains(ada, "hello") {
		t.Error("the section followed the wrong foreign key")
	}
}

func TestStartupWarnsWhenARelatedSectionWouldScan(t *testing.T) {
	var logs bytes.Buffer
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), relatedSchema); err != nil {
		t.Fatal(err)
	}
	build := func(child string) string {
		logs.Reset()
		admin, err := pgdesk.New(pool,
			pgdesk.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
			pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
			pgdesk.WithAuthorizer(pgdesk.AllowAll),
			pgdesk.WithResource("rel_sessions", func(r *pgdesk.Resource) { r.Related(child) }),
			pgdesk.WithResource(child, nil),
		)
		if err != nil {
			t.Fatal(err)
		}
		_ = admin.Close()
		return logs.String()
	}
	if out := build("rel_noindex"); !strings.Contains(out, "will scan") || !strings.Contains(out, "session_id") {
		t.Errorf("no warning for an unindexed reference: %s", out)
	}
	if out := build("rel_turns"); strings.Contains(out, "will scan") {
		t.Errorf("a warning for an indexed reference (its key begins with it): %s", out)
	}
}

func TestOneSectionThatFailsDoesNotTakeThePageDown(t *testing.T) {
	admin, err := relatedAdmin(t, relatedConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), `ALTER TABLE rel_turns RENAME COLUMN answer TO reply`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `ALTER TABLE rel_turns RENAME COLUMN reply TO answer`) })
	rec := detail(admin, adaSession)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Ada interview") {
		t.Fatalf("%d: the page failed with its section", rec.Code)
	}
}

func TestStartupWarnsWhenTheFullListCannotBeLinked(t *testing.T) {
	var logs bytes.Buffer
	pool := capPool(t)
	if _, err := pool.Exec(context.Background(), relatedSchema); err != nil {
		t.Fatal(err)
	}
	build := func(filter string) string {
		logs.Reset()
		admin, err := pgdesk.New(pool,
			pgdesk.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
			pgdesk.WithSecretKey([]byte("integration-test-secret-key-000000")),
			pgdesk.WithAuthorizer(pgdesk.AllowAll),
			pgdesk.WithResource("rel_sessions", func(r *pgdesk.Resource) { r.Related("rel_turns") }),
			pgdesk.WithResource("rel_turns", func(r *pgdesk.Resource) { r.Filters(filter) }),
		)
		if err != nil {
			t.Fatal(err)
		}
		_ = admin.Close()
		return logs.String()
	}
	if out := build("tenant"); !strings.Contains(out, "no full-list link") {
		t.Errorf("no warning without a filter on the reference: %s", out)
	}
	if out := build("session_id"); strings.Contains(out, "no full-list link") {
		t.Errorf("warned although the filter exists: %s", out)
	}
}
