package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
	"github.com/stripe/stripe-go/v75"
	"github.com/stripe/stripe-go/v75/checkout/session"
	"github.com/stripe/stripe-go/v75/customer"
	"github.com/stripe/stripe-go/v75/subscription"
	"github.com/stripe/stripe-go/v75/webhook"

	"github.com/TheLab-ms/profile/conf"
	"github.com/TheLab-ms/profile/keycloak"
	"github.com/TheLab-ms/profile/moodle"
	"github.com/TheLab-ms/profile/stripeutil"
)

//go:embed assets/*
var assets embed.FS

//go:embed templates/*.html
var rawTemplates embed.FS

var templates *template.Template

func init() {
	// Parse the embedded templates once during initialization
	var err error
	templates, err = template.ParseFS(rawTemplates, "templates/*")
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	env := &conf.Env{}
	if err := envconfig.Process("", env); err != nil {
		log.Fatal(err)
	}
	stripe.Key = env.StripeKey

	kc := keycloak.New(env)
	m := moodle.New(env)
	priceCache := stripeutil.StartPriceCache()

	// Redirect from / to /profile
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
	})

	// Signup view and registration POST handler
	http.HandleFunc("/signup", newSignupViewHandler(kc))
	http.HandleFunc("/signup/register", newRegistrationFormHandler(kc))

	// Profile view and associated form POST handlers
	http.HandleFunc("/profile", newProfileViewHandler(kc, priceCache))
	http.HandleFunc("/profile/keyfob", newKeyfobFormHandler(kc))
	http.HandleFunc("/profile/contact", newContactInfoFormHandler(kc))
	http.HandleFunc("/profile/stripe", newStripeCheckoutHandler(env, kc))
	http.HandleFunc("/profile/cancel", newCancelHandler(env, kc))

	// Webhooks
	http.HandleFunc("/webhooks/docuseal", newDocusealWebhookHandler(kc))
	http.HandleFunc("/webhooks/stripe", newStripeWebhookHandler(env, kc))
	http.HandleFunc("/webhooks/moodle", newMoodleWebhookHandler(env, kc, m))

	// Embed (into the compiled binary) and serve any files from the assets directory
	http.Handle("/assets/", http.FileServer(http.FS(assets)))

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func newSignupViewHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templates.ExecuteTemplate(w, "signup.html", map[string]any{"page": "signup"})
	}
}

func newRegistrationFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		viewData := map[string]any{"page": "signup", "success": true}

		err := kc.RegisterUser(r.Context(), r.FormValue("email"))

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

		templates.ExecuteTemplate(w, "signup.html", viewData)
	}
}

func newProfileViewHandler(kc *keycloak.Keycloak, pc stripeutil.PriceCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := kc.GetUserAtETag(r.Context(), getUserID(r), r.URL.Query().Get("etag"))
		if err != nil {
			renderSystemError(w, "error while fetching user: %s", err)
			return
		}

		viewData := map[string]any{
			"page":   "profile",
			"user":   user,
			"prices": pc(),
		}
		if user.StripeCancelationTime > 0 {
			viewData["expiration"] = time.Unix(user.StripeCancelationTime, 0).Format("01/02/06")
		}

		templates.ExecuteTemplate(w, "profile.html", viewData)
	}
}

func newKeyfobFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fobIdStr := r.FormValue("fobid")
		fobID, err := strconv.Atoi(fobIdStr)
		if (fobIdStr != "" && err != nil) || fobID == 0 {
			http.Error(w, "invalid fobid", 400)
			return
		}

		err = kc.UpdateUserFobID(r.Context(), getUserID(r), fobID)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func newContactInfoFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		first := r.FormValue("first")
		last := r.FormValue("last")
		if first == "" || last == "" {
			http.Error(w, "missing name", 400)
			return
		}

		err := kc.UpdateUserName(r.Context(), getUserID(r), first, last)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func newStripeCheckoutHandler(env *conf.Env, kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := kc.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user from Keycloak: %s", err)
			return
		}

		etag := uuid.Must(uuid.NewRandom()).String()
		checkoutParams := &stripe.CheckoutSessionParams{
			CustomerEmail: &user.Email,
			SuccessURL:    stripe.String(env.SelfURL + "/profile?etag=" + etag),
			CancelURL:     stripe.String(env.SelfURL + "/profile"),
		}
		checkoutParams.Context = r.Context()

		// If there is an active payment on record for this user, start a session to update the payment credentials.
		// Otherwise start the initial Stripe checkout session.
		if user.ActivePayment {
			checkoutParams.Mode = stripe.String(string(stripe.CheckoutSessionModeSetup))
			checkoutParams.PaymentMethodTypes = stripe.StringSlice([]string{
				"card",
			})
		} else {
			checkoutParams.Mode = stripe.String(string(stripe.CheckoutSessionModeSubscription))
			checkoutParams.AllowPromotionCodes = stripe.Bool(true)
			checkoutParams.LineItems = []*stripe.CheckoutSessionLineItemParams{{
				Price:    stripe.String(r.URL.Query().Get("price")),
				Quantity: stripe.Int64(1),
			}}
			checkoutParams.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{
				Metadata: map[string]string{"etag": etag},
			}
		}
		s, err := session.New(checkoutParams)
		if err != nil {
			renderSystemError(w, "error while creating session: %s", err)
			return
		}

		http.Redirect(w, r, s.URL, http.StatusSeeOther)
	}
}

func newCancelHandler(env *conf.Env, kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := kc.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user from Keycloak: %s", err)
			return
		}

		etag := uuid.Must(uuid.NewRandom()).String()
		_, err = subscription.Update(user.StripeSubscriptionID, &stripe.SubscriptionParams{
			CancelAtPeriodEnd: stripe.Bool(true),
			Metadata:          map[string]string{"etag": etag},
		})
		if err != nil {
			renderSystemError(w, "error while canceling Stripe subscription: %s", err)
			return
		}

		time.Sleep(time.Second / 2) // hack to avoid webhook race condition
		http.Redirect(w, r, "/profile?etag="+etag, http.StatusSeeOther)
	}
}

func newDocusealWebhookHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := struct {
			Data struct {
				Email string `json:"email"`
			} `json:"data"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			log.Printf("invalid json sent to docuseal webhook endpoint: %s", err)
			w.WriteHeader(400)
			return
		}

		log.Printf("got docuseal webhook for user %s", body.Data.Email)
		err := kc.UpdateUserWaiverState(r.Context(), body.Data.Email)
		if err != nil {
			log.Printf("error while updating user's waiver state: %s", err)
			w.WriteHeader(500)
			return
		}
	}
}

func newStripeWebhookHandler(env *conf.Env, kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error while reading Stripe webhook body: %s", err)
			w.WriteHeader(503)
			return
		}

		event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), env.StripeWebhookKey)
		if err != nil {
			log.Printf("error while constructing Stripe webhook event: %s", err)
			w.WriteHeader(400)
			return
		}

		switch event.Type {
		case "customer.subscription.deleted":
		case "customer.subscription.updated":
		case "customer.subscription.created":
		default:
			log.Printf("unhandled Stripe webhook event type: %s", event.Type)
			return
		}

		sub := &stripe.Subscription{}
		err = json.Unmarshal(event.Data.Raw, sub)
		if err != nil {
			log.Printf("got invalid Stripe webhook event: %s", err)
			w.WriteHeader(400)
			return
		}

		customer, err := customer.Get(sub.Customer.ID, &stripe.CustomerParams{})
		if err != nil {
			log.Printf("unable to get Stripe customer object: %s", err)
			w.WriteHeader(500)
			return
		}
		log.Printf("got Stripe subscription event for member %q, state=%s", customer.Email, sub.Status)

		err = kc.UpdateUserStripeInfo(r.Context(), customer, sub)
		if err != nil {
			log.Printf("error while updating Keycloak for Stripe subscription webhook event: %s", err)
			w.WriteHeader(500)
			return
		}
	}
}

func newMoodleWebhookHandler(env *conf.Env, kc *keycloak.Keycloak, m *moodle.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := struct {
			EventName string `json:"eventname"`
			Host      string `json:"host"`
			Token     string `json:"token"`
			CourseID  string `json:"courseid"`
			UserID    string `json:"relateduserid"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			log.Printf("invalid json sent to moodle webhook endpoint: %s", err)
			w.WriteHeader(400)
			return
		}
		if body.Token != env.MoodleSecret {
			log.Printf("invalid moodle webhook secret")
			w.WriteHeader(400)
			return
		}
		switch body.EventName {
		case "\\core\\event\\course_completed":
			log.Printf("got moodle course completion submission webhook")

			// Lookup user by moodle ID to get email address
			moodleUser, err := m.GetUserByID(body.UserID)
			if err != nil {
				log.Printf("error while looking up user by moodle ID: %s", err)
				w.WriteHeader(500)
				return
			}
			// Use email address to lookup user in Keycloak
			user, err := kc.GetUserByEmail(r.Context(), moodleUser.Email)
			if err != nil {
				log.Printf("error while looking up user by email: %s", err)
				w.WriteHeader(500)
				return
			}

			err = kc.AddUserToGroup(r.Context(), user.ID, "6e413212-c1d8-4bc9-abb3-51944ca35327")
			if err != nil {
				log.Printf("error while adding user to group: %s", err)
				w.WriteHeader(500)
				return
			}

		default:
			log.Printf("unhandled moodle webhook event type: %s, ignoring", body.EventName)
		}

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
