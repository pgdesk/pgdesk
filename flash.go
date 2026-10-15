package pgdesk

import (
	"encoding/json"
	"net/http"
)

const flashCookieName = "pgdesk_flash"

type flashPayload struct {
	Level   string `json:"l"`
	Message string `json:"m"`
}

func (a *Admin) setFlash(w http.ResponseWriter, level, message string) {
	if a.signer == nil {
		return
	}
	payload, err := json.Marshal(flashPayload{Level: level, Message: message})
	if err != nil {
		return
	}
	http.SetCookie(w, a.newCookie(flashCookieName, a.signer.Seal(payload), a.cfg.basePath, 30))
}

func (a *Admin) takeFlash(w http.ResponseWriter, r *http.Request) []flashMsg {
	if a.signer == nil {
		return nil
	}
	c, err := r.Cookie(flashCookieName)
	if err != nil {
		return nil
	}

	http.SetCookie(w, a.newCookie(flashCookieName, "", a.cfg.basePath, -1))

	payload, err := a.signer.Open(c.Value)
	if err != nil {
		return nil
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
