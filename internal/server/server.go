package server

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/time/rate"

	"github.com/TheLab-ms/profile/internal/chatbot"
	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/events"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/payment"
	"github.com/TheLab-ms/profile/internal/reporting"
)

type Server struct {
	Env         *conf.Env
	Keycloak    *keycloak.Keycloak
	PriceCache  *payment.PriceCache
	EventsCache *events.EventCache
	Assets      fs.FS
	Templates   *template.Template
}

func (s *Server) NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/signup", s.newSignupViewHandler())
	mux.HandleFunc("/signup/register", s.newRegistrationFormHandler())
	mux.HandleFunc("/profile", s.newProfileViewHandler())
	mux.HandleFunc("/profile/contact", s.newContactInfoFormHandler())
	mux.HandleFunc("/profile/stripe", s.newStripeCheckoutHandler())
	mux.HandleFunc("/docuseal", s.newDocusealRedirectHandler())
	mux.HandleFunc("/fobqr", s.newFobQRHandler())
	mux.HandleFunc("/secrets", s.newSecretIndexHandler())
	mux.HandleFunc("/secrets/encrypt", s.newSecretEncryptionHandler())
	mux.HandleFunc("/link-discord", s.newDiscordLinkHandler())
	mux.HandleFunc("/webhooks/docuseal", s.newDocusealWebhookHandler())
	mux.HandleFunc("/webhooks/stripe", s.newStripeWebhookHandler())
	mux.HandleFunc("/admin/dump", onlyLeadership(s.newAdminDumpHandler()))
	mux.HandleFunc("/admin/assign-fob", onlyLeadership(s.newAssignFobHandler()))
	mux.HandleFunc("/admin/apply-discount", onlyLeadership(s.newApplyDiscountHandler()))
	mux.HandleFunc("/api/events", s.newListEventsHandler())
	mux.HandleFunc("/api/prices", s.newPricingHandler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})
	mux.Handle("/assets/", http.FileServer(http.FS(s.Assets)))
	return mux
}

func (s *Server) newSignupViewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.Templates.ExecuteTemplate(w, "signup.html", map[string]any{"page": "signup"})
	}
}

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

		reporting.DefaultSink.Publish(email, "Signup", "user created an account")
		s.Templates.ExecuteTemplate(w, "signup.html", viewData)
	}
}

func (s *Server) newProfileViewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		etagString := r.URL.Query().Get("i")
		etag, _ := strconv.ParseInt(etagString, 10, 0)
		user, err := s.Keycloak.GetUserAtETag(r.Context(), getUserID(r), etag)
		if err != nil {
			renderSystemError(w, "error while fetching user: %s", err)
			return
		}

		viewData := map[string]any{
			"page":            "profile",
			"user":            user,
			"prices":          payment.CalculateDiscounts(user, s.PriceCache.GetPrices()),
			"migratedAccount": user.LastPaypalTransactionTime != time.Time{},
			"stripePending":   etagString != "" && user.StripeETag < etag,
		}
		if user.StripeCancelationTime > 0 {
			viewData["expiration"] = time.Unix(user.StripeCancelationTime, 0).Format("01/02/06")
		}

		s.Templates.ExecuteTemplate(w, "profile.html", viewData)
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

		err = s.Keycloak.UpdateUserName(r.Context(), user, first, last)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		reporting.DefaultSink.Publish(user.Email, "UpdatedContactInfo", "user updated their contact information")
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (s *Server) newFobQRHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}
		url := fmt.Sprintf("%s/admin/assign-fob?email=%s", s.Env.SelfURL, user.Email)

		png, err := qrcode.Encode(url, qrcode.Medium, 512)
		if err != nil {
			renderSystemError(w, "generating QR code: %s", err)
			return
		}

		w.Header().Add("Content-Type", "image/png")
		w.Write(png)
	}
}

func (s *Server) newDiscordLinkHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		discordUserID := r.URL.Query().Get("user")
		sig := chatbot.GenerateHMAC(discordUserID, s.Env.DiscordBotToken)
		if r.URL.Query().Get("sig") != sig {
			http.Error(w, "invalid signature", 400)
			return
		}

		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		err = s.Keycloak.UpdateDiscordLink(r.Context(), user, discordUserID)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}
		reporting.DefaultSink.Publish(user.Email, "DiscordLinked", "member linked discord account %s", discordUserID)
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
	}
}

func onlyLeadership(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("X-Forwarded-Groups"), "leadership") {
			http.Error(w, "unauthorized", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// getUserID allows the oauth2proxy header to be overridden for testing.
func getUserID(r *http.Request) string {
	user := r.Header.Get("X-Forwarded-Preferred-Username")
	if user == "" {
		return os.Getenv("TESTUSERID")
	}
	return user
}

func renderSystemError(w http.ResponseWriter, msg string, args ...any) {
	log.Printf(msg, args...)
	http.Error(w, "system error", 500)
}
