package pgdesk

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/csrf"
)

func flashAdmin(t *testing.T) *Admin {
	t.Helper()
	signer, err := csrf.NewSigner([]byte("flash-secret-key-000000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	return &Admin{cfg: defaultConfig(), signer: signer}
}

func TestFlashRoundTrip(t *testing.T) {
	a := flashAdmin(t)

	setRec := httptest.NewRecorder()
	a.setFlash(setRec, "info", "User created.")
	cookie := setRec.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != flashCookieName {
		t.Fatalf("expected one flash cookie, got %v", cookie)
	}

	takeRec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.AddCookie(cookie[0])
	flashes := a.takeFlash(takeRec, req)
	if len(flashes) != 1 || flashes[0].Message != "User created." || flashes[0].Level != "info" {
		t.Fatalf("takeFlash returned %+v", flashes)
	}

	cleared := takeRec.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("takeFlash should clear the cookie, got %+v", cleared)
	}
}

func TestFlashRejectsTamperedCookie(t *testing.T) {
	a := flashAdmin(t)
	req := httptest.NewRequest("GET", "/admin/users", nil)

	req.AddCookie(&http.Cookie{Name: flashCookieName, Value: "eyJsIjoiZXJyb3IiLCJtIjoiaGFja2VkIn0.zzzz"})
	if f := a.takeFlash(httptest.NewRecorder(), req); f != nil {
		t.Fatalf("tampered flash should be ignored, got %+v", f)
	}
}

func TestFlashNoopWithoutSigner(t *testing.T) {
	a := &Admin{cfg: defaultConfig()}
	rec := httptest.NewRecorder()
	a.setFlash(rec, "info", "x")
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("setFlash without a signer must not write a cookie")
	}
}

func TestFlashMessageEscapedInRender(t *testing.T) {

	a := testAdmin(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/users", nil)
	req = req.WithContext(withFlash(req.Context(), []flashMsg{{Level: "info", Message: "<script>alert(1)</script>"}}))
	a.renderError(rec, req, http.StatusOK, "ok")
	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Fatalf("flash markup was not escaped:\n%s", rec.Body.String())
	}
}
