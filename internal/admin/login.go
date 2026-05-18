package admin

import (
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/radarnex/httpcatch/internal/config"
)

var loginTmpl = template.Must(template.ParseFS(uiFS, "ui/login.html"))

// authHandlers groups the dependencies shared by the login page, login POST,
// and logout handlers so they can be registered as methods rather than deeply
// nested closures.
type authHandlers struct {
	cfg    config.AdminConfig
	store  *SessionStore
	logger *slog.Logger
}

// isSafePath returns true when next is a relative path that starts with a
// single slash — i.e. not empty, not protocol-relative (//), not an absolute
// URL. This prevents an open redirect via the ?next= parameter.
func isSafePath(next string) bool {
	return strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//")
}

// loginPageHandler serves the HTML login form. The optional ?next= and ?err=
// query params are injected via html/template so they are HTML-escaped.
func (a *authHandlers) loginPageHandler(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if !isSafePath(next) {
		next = ""
	}
	data := struct {
		Next  string
		Error bool
	}{
		Next:  next,
		Error: r.URL.Query().Get("err") == "1",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := loginTmpl.Execute(w, data); err != nil {
		a.logger.Error("login page render failed", "err", err)
	}
}

// loginPostHandler validates the submitted token and, on success, issues a
// session cookie and redirects. On failure it re-renders the login form with a
// generic error; the submitted token is never echoed in the response.
func (a *authHandlers) loginPostHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	if !checkBearer(token, a.cfg.Token) {
		next := r.FormValue("next")
		if !isSafePath(next) {
			next = ""
		}
		data := struct {
			Next  string
			Error bool
		}{
			Next:  next,
			Error: true,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		if err := loginTmpl.Execute(w, data); err != nil {
			a.logger.Error("login page render failed", "err", err)
		}
		return
	}

	sess, err := a.store.Create(a.cfg.SessionTTL)
	if err != nil {
		a.logger.Error("session create failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		MaxAge:   int(a.cfg.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   a.cfg.SessionSecure,
		SameSite: http.SameSiteLaxMode,
	})

	next := r.FormValue("next")
	if !isSafePath(next) {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// logoutHandler revokes the current session cookie and redirects to the login
// page. A missing or bogus cookie is silently ignored — the clearing cookie is
// always set regardless.
func (a *authHandlers) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		a.store.Revoke(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.cfg.SessionSecure,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
