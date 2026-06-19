package pgdesk

import (
	"encoding/json"
	"net/http"
)

// Flash messages give one-shot feedback across the post-mutation redirect (e.g.
// "User created"). Because sessions belong to the host, pgdesk carries flashes in
// its own dedicated, signed cookie — signed so a tampered cookie cannot inject
// misleading text, and still escaped as untrusted template data on render (F1).
//
// The cookie is short-lived, HttpOnly, SameSite=Lax, and scoped to the base path.
// Flashes are only used on mutation paths, which already require a signing key,
// so setFlash is a no-op without one.

const flashCookieName = "pgdesk_flash"

// flashPayload is the JSON shape stored in the (signed) cookie.
type flashPayload struct {
	Level   string `json:"l"`
	Message string `json:"m"`
}

// setFlash writes a signed flash cookie to be shown on the next GET. level is
// "info" or "error"; message is developer-authored, never user input.
func (a *Admin) setFlash(w http.ResponseWriter, level, message string) {
	if a.signer == nil {
		return
	}
	payload, err := json.Marshal(flashPayload{Level: level, Message: message})
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    a.signer.Seal(payload),
		Path:     a.cfg.basePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30,
	})
}

// takeFlash reads and immediately clears the flash cookie, returning the parsed
// messages. A missing, malformed, or unsigned cookie yields no flashes and is not
// an error (fail-closed on integrity, silent on absence).
func (a *Admin) takeFlash(w http.ResponseWriter, r *http.Request) []flashMsg {
	if a.signer == nil {
		return nil
	}
	c, err := r.Cookie(flashCookieName)
	if err != nil {
		return nil
	}
	// Always clear the cookie once we've seen it, valid or not.
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     a.cfg.basePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	payload, err := a.signer.Open(c.Value)
	if err != nil {
		return nil // tampered or stale signature → ignore
	}
	var fp flashPayload
	if err := json.Unmarshal(payload, &fp); err != nil {
		return nil
	}
	level := fp.Level
	if level != "error" {
		level = "info"
	}
	return []flashMsg{{Level: level, Message: fp.Message}}
}
