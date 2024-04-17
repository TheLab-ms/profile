package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
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

	"golang.org/x/time/rate"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/events"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/payment"
	"github.com/TheLab-ms/profile/internal/pricing"
	"github.com/TheLab-ms/profile/internal/reporting"
)

type Server struct {
	Env         *conf.Env
	Keycloak    *keycloak.Keycloak
	PriceCache  *payment.PriceCache
	EventsCache *events.EventCache
	Assets      fs.FS
	Templates   *template.Template

	fobUpdateMut sync.Mutex
}

func (s *Server) NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/signup", s.newSignupViewHandler())
	mux.HandleFunc("/signup/register", s.newRegistrationFormHandler())
	mux.HandleFunc("/profile", s.newProfileViewHandler())
	mux.HandleFunc("/profile/keyfob", s.newKeyfobFormHandler())
	mux.HandleFunc("/profile/contact", s.newContactInfoFormHandler())
	mux.HandleFunc("/profile/stripe", s.newStripeCheckoutHandler())
	mux.HandleFunc("/docuseal", s.newDocusealRedirectHandler())
	mux.HandleFunc("/secrets", s.newSecretIndexHandler())
	mux.HandleFunc("/secrets/encrypt", s.newSecretEncryptionHandler())
	mux.HandleFunc("/webhooks/docuseal", s.newDocusealWebhookHandler())
	mux.HandleFunc("/webhooks/stripe", payment.NewWebhookHandler(s.Env, s.Keycloak, s.PriceCache))
	mux.HandleFunc("/admin/dump", onlyLeadership(s.newAdminDumpHandler()))
	mux.HandleFunc("/admin/enable-building-access", onlyLeadership(s.newEnableBuildingAccessHandler()))
	mux.HandleFunc("/api/events", s.newListEventsHandler())
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
			"prices":          pricing.CalculateDiscounts(user, s.PriceCache.GetPrices()),
			"migratedAccount": user.LastPaypalTransactionTime != time.Time{},
			"stripePending":   etagString != "" && user.StripeETag < etag,
		}
		if user.StripeCancelationTime > 0 {
			viewData["expiration"] = time.Unix(user.StripeCancelationTime, 0).Format("01/02/06")
		}

		s.Templates.ExecuteTemplate(w, "profile.html", viewData)
	}
}

func (s *Server) newKeyfobFormHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fobIdStr := r.FormValue("fobid")
		fobID, err := strconv.Atoi(fobIdStr)
		if fobIdStr != "" && err != nil {
			http.Error(w, "invalid fobid", 400)
			return
		}

		// We can't safely allow concurrent key fob ID update operations,
		// since Keycloak doesn't support optimistic concurrency control.
		//
		// This is because we need to first check if a fob is already in
		// use before assigning it. Without any concurrency controls it
		// would be possible to use timing attacks to re-assign existing
		// fobs to multiple accounts.
		//
		// So let's set a reasonable timeout to avoid one user blocking
		// everyone else's ability to update their fob.
		ctx, cancel := context.WithTimeout(r.Context(), time.Second*30)
		defer cancel()

		s.fobUpdateMut.Lock()
		defer s.fobUpdateMut.Unlock()

		user, err := s.Keycloak.GetUser(ctx, getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}
		if user.FobID == fobID {
			// fob didn't change
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		if fobIdStr != "" {
			conflict, err := s.Keycloak.BadgeIDInUse(ctx, fobID)
			if err != nil {
				renderSystemError(w, "error while checking if badge ID is in use: %s", err)
				return
			}
			if conflict {
				http.Error(w, "that badge ID is already in use", 400)
				return
			}
		}

		err = s.Keycloak.UpdateUserFobID(ctx, user, fobID)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		reporting.DefaultSink.Publish(user.Email, "UpdatedFobID", "user updated their fob ID from %d to %d", user.FobID, fobID)
		http.Redirect(w, r, "/", http.StatusSeeOther)
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

func (s *Server) newAdminDumpHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := s.Keycloak.ListUsers(r.Context())
		if err != nil {
			log.Printf("error while listing users: %s", err)
			w.WriteHeader(500)
			return
		}

		if r.Header.Get("Accept") == "application/json" {
			w.Header().Add("Content-Type", "application/json")
			enc := json.NewEncoder(w)
			enc.SetIndent("  ", "  ")
			enc.Encode(&users)
			return
		}

		cw := csv.NewWriter(w)
		cw.Write([]string{
			"First", "Last", "Email", "Email Verified", "Waiver Signed",
			"Stripe ID", "Stripe Subscription ID", "Discount Type", "Keyfob ID",
			"Active Member", "Signup Timestamp", "Paypal Migration",
		})

		for _, user := range users {
			cw.Write([]string{
				user.First, user.Last, user.Email,
				strconv.FormatBool(user.EmailVerified), strconv.FormatBool(user.SignedWaiver),
				user.StripeCustomerID, user.StripeSubscriptionID, user.DiscountType,
				strconv.Itoa(user.FobID), strconv.FormatBool(user.ActiveMember),
				user.SignupTime.Format(time.RFC3339), strconv.FormatBool(user.PaypalSubscriptionID != ""),
			})
		}
		cw.Flush() // avoid buffering entire response in memory
	}
}

func (s *Server) newEnableBuildingAccessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUserByEmail(r.Context(), r.FormValue("email"))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		err = s.Keycloak.EnableUserBuildingAccess(r.Context(), user, getUserID(r))
		if err != nil {
			renderSystemError(w, "error while writing to Keycloak: %s", err)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("👍"))
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
