package server

import (
	"errors"
	"log"
	"net/http"
	"net/mail"
	"sync"

	"github.com/TheLab-ms/profile"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/reporting"
	"golang.org/x/time/rate"
)

func (s *Server) newRegistrationFormHandler() http.HandlerFunc {
	rateLimiter := rate.NewLimiter(1, 2)
	lock := sync.Mutex{}
	return func(w http.ResponseWriter, r *http.Request) {
		if err := rateLimiter.Wait(r.Context()); err != nil {
			log.Printf("rate limiter error: %s", err)
		}
		viewData := map[string]any{"page": "signup", "success": true}

		email := r.FormValue("email")
		if _, err := mail.ParseAddress(email); err != nil {
			http.Error(w, "invalid email address", 400)
			return
		}

		lock.Lock()
		defer lock.Unlock()
		err := s.Keycloak.RegisterUser(r.Context(), email)

		// Limit the number of accounts with unconfirmed email addresses to avoid spam/abuse
		if errors.Is(err, keycloak.ErrLimitExceeded) {
			err = nil
			viewData["limitExceeded"] = true
			viewData["success"] = false
		}

		// Currently we just render a descriptive error message when the user already exists.
		// Consider having an option to start the password reset flow, or maybe do so by default.
		if errors.Is(err, keycloak.ErrConflict) {
			err = nil
			viewData["conflict"] = true
			viewData["success"] = false
		}

		if err != nil {
			renderSystemError(w, "error while registering user: %s", err)
			return
		}

		reporting.DefaultSink.Eventf(email, "Signup", "user created an account")
		profile.Templates.ExecuteTemplate(w, "signup.html", viewData)
	}
}

func (s *Server) newContactInfoFormHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		first := r.FormValue("first")
		last := r.FormValue("last")
		if first == "" || last == "" {
			http.Error(w, "missing name", 400)
			return
		}
		if len(first) > 256 || len(last) > 256 {
			http.Error(w, "name is too long", 400)
			return
		}

		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		if user.First == first && user.Last == last {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return // nothing changed
		}

		user.First = first
		user.Last = last
		err = s.Keycloak.WriteUser(r.Context(), user)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		reporting.DefaultSink.Eventf(user.Email, "UpdatedContactInfo", "user updated their contact information")
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
