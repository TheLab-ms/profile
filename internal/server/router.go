package server

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/TheLab-ms/profile"
	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/TheLab-ms/profile/internal/events"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/payment"
	"github.com/TheLab-ms/profile/internal/paypal"
)

type Server struct {
	Env         *conf.Env
	Keycloak    *keycloak.Keycloak[*datamodel.User]
	Paypal      *paypal.Client
	PriceCache  *payment.PriceCache
	EventsCache *events.EventCache
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
	mux.HandleFunc("/api/events", s.newListEventsHandler())
	mux.HandleFunc("/api/prices", s.newPricingHandler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})
	mux.Handle("/assets/", http.FileServer(http.FS(profile.Assets)))
	return mux
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
