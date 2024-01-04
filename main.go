package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/kelseyhightower/envconfig"
	"github.com/stripe/stripe-go/v75"
	billingsession "github.com/stripe/stripe-go/v75/billingportal/session"
	"github.com/stripe/stripe-go/v75/checkout/session"
	"github.com/stripe/stripe-go/v75/customer"
	"github.com/stripe/stripe-go/v75/webhook"
	"golang.org/x/time/rate"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/reporting"
	"github.com/TheLab-ms/profile/internal/stripeutil"
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
	err := envconfig.Process("", env)
	if err != nil {
		log.Fatal(err)
	}
	stripe.Key = env.StripeKey

	reporting.DefaultSink, err = reporting.NewSink(env)
	if err != nil {
		log.Fatal(err)
	}

	kc := keycloak.New(env)
	go kc.RunReportingLoop()

	priceCache := &stripeutil.PriceCache{}
	priceCache.Start()

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
	http.HandleFunc("/profile/stripe", newStripeCheckoutHandler(env, kc, priceCache))

	// Webhooks
	http.HandleFunc("/webhooks/docuseal", newDocusealWebhookHandler(kc))
	http.HandleFunc("/webhooks/stripe", newStripeWebhookHandler(env, kc, priceCache))

	// Embed (into the compiled binary) and serve any files from the assets directory
	http.Handle("/assets/", http.FileServer(http.FS(assets)))

	// Various leadership-only admin endpoints
	http.HandleFunc("/admin/dump", onlyLeadership(newAdminDumpHandler(kc)))

	// k8s readiness probe handler
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func newSignupViewHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templates.ExecuteTemplate(w, "signup.html", map[string]any{"page": "signup"})
	}
}

func newRegistrationFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	rateLimiter := rate.NewLimiter(1, 2)
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

		// TODO: Limit concurrency to avoid attack vector
		err := kc.RegisterUser(r.Context(), email)

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
		templates.ExecuteTemplate(w, "signup.html", viewData)
	}
}

func newProfileViewHandler(kc *keycloak.Keycloak, pc *stripeutil.PriceCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		etagString := r.URL.Query().Get("i")
		etag, _ := strconv.ParseInt(etagString, 10, 0)
		user, err := kc.GetUserAtETag(r.Context(), getUserID(r), etag)
		if err != nil {
			renderSystemError(w, "error while fetching user: %s", err)
			return
		}

		viewData := map[string]any{
			"page":            "profile",
			"user":            user,
			"prices":          calculateDiscounts(user, pc.GetPrices()),
			"migratedAccount": user.LastPaypalTransactionTime != time.Time{},
			"stripePending":   etagString != "" && user.StripeETag < etag,
		}
		if user.StripeCancelationTime > 0 {
			viewData["expiration"] = time.Unix(user.StripeCancelationTime, 0).Format("01/02/06")
		}

		templates.ExecuteTemplate(w, "profile.html", viewData)
	}
}

var fobUpdateMut sync.Mutex

func newKeyfobFormHandler(kc *keycloak.Keycloak) http.HandlerFunc {
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

		fobUpdateMut.Lock()
		defer fobUpdateMut.Unlock()

		user, err := kc.GetUser(ctx, getUserID(r))
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
			conflict, err := kc.BadgeIDInUse(ctx, fobID)
			if err != nil {
				renderSystemError(w, "error while checking if badge ID is in use: %s", err)
				return
			}
			if conflict {
				http.Error(w, "that badge ID is already in use", 400)
				return
			}
		}

		err = kc.UpdateUserFobID(ctx, user, fobID)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		reporting.DefaultSink.Publish(user.Email, "UpdatedFobID", "user updated their fob ID from %d to %d", user.FobID, fobID)
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
		if len(first) > 256 || len(last) > 256 {
			http.Error(w, "name is too long", 400)
			return
		}

		user, err := kc.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		if user.First == first && user.Last == last {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return // nothing changed
		}

		err = kc.UpdateUserName(r.Context(), user, first, last)
		if err != nil {
			renderSystemError(w, "error while updating user: %s", err)
			return
		}

		reporting.DefaultSink.Publish(user.Email, "UpdatedContactInfo", "user updated their contact information")
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func newStripeCheckoutHandler(env *conf.Env, kc *keycloak.Keycloak, pc *stripeutil.PriceCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := kc.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user from Keycloak: %s", err)
			return
		}
		etag := strconv.FormatInt(user.StripeETag+1, 10)

		// If there is an active payment on record for this user, start a session to manage the subscription.
		if user.ActivePayment {
			sessionParams := &stripe.BillingPortalSessionParams{
				Customer:  stripe.String(user.StripeCustomerID),
				ReturnURL: stripe.String(env.SelfURL + "/profile"),
			}
			sessionParams.Context = r.Context()

			s, err := billingsession.New(sessionParams)
			if err != nil {
				renderSystemError(w, "error while creating session: %s", err)
				return
			}

			http.Redirect(w, r, s.URL, http.StatusSeeOther)
			return
		}

		// No active payment - sign them up!
		checkoutParams := &stripe.CheckoutSessionParams{
			Mode:          stripe.String(string(stripe.CheckoutSessionModeSubscription)),
			CustomerEmail: &user.Email,
			SuccessURL:    stripe.String(env.SelfURL + "/profile?i=" + etag),
			CancelURL:     stripe.String(env.SelfURL + "/profile"),
		}
		checkoutParams.Context = r.Context()

		// Calculate specific pricing based on the member's profile
		priceID := r.URL.Query().Get("price")
		checkoutParams.LineItems = calculateLineItems(user, priceID, pc)
		checkoutParams.Discounts = calculateDiscount(user, priceID, pc)
		if checkoutParams.Discounts == nil {
			// Stripe API doesn't allow Discounts and AllowPromotionCodes to be set
			checkoutParams.AllowPromotionCodes = stripe.Bool(true)
		}

		checkoutParams.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata:           map[string]string{"etag": etag},
			BillingCycleAnchor: calculateBillingCycleAnchor(user), // This enables migration from paypal
		}
		if checkoutParams.SubscriptionData.BillingCycleAnchor != nil {
			// In this case, the member is already paid up - don't make them pay for the currenet period again
			checkoutParams.SubscriptionData.ProrationBehavior = stripe.String("none")
		}

		s, err := session.New(checkoutParams)
		if err != nil {
			renderSystemError(w, "error while creating session: %s", err)
			return
		}

		reporting.DefaultSink.Publish(user.Email, "StartedStripeCheckout", "started Stripe checkout session: %s", s.ID)
		http.Redirect(w, r, s.URL, http.StatusSeeOther)
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

		user, err := kc.GetUserByEmail(r.Context(), body.Data.Email)
		if err != nil {
			log.Printf("unable to get user by email address: %s", err)
			w.WriteHeader(500)
			return
		}

		err = kc.UpdateUserWaiverState(r.Context(), user)
		if err != nil {
			log.Printf("error while updating user's waiver state: %s", err)
			w.WriteHeader(500)
			return
		}

		reporting.DefaultSink.Publish(body.Data.Email, "SignedWaiver", "user signed waiver")
	}
}

func newStripeWebhookHandler(env *conf.Env, kc *keycloak.Keycloak, pc *stripeutil.PriceCache) http.HandlerFunc {
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

		if strings.HasPrefix(string(event.Type), "price.") || strings.HasPrefix(string(event.Type), "coupon.") {
			log.Printf("refreshing Stripe caches because a webhook was received that suggests things have changed")
			pc.Refresh()
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

		user, err := kc.GetUserByEmail(r.Context(), customer.Email)
		if err != nil {
			log.Printf("unable to get user by email address: %s", err)
			w.WriteHeader(500)
			return
		}

		// Clean up old paypal sub if it still exists
		if env.PaypalClientID != "" && env.PaypalClientSecret != "" {
			if user.PaypalSubscriptionID != "" { // this is removed by UpdateUserStripeInfo
				err := cancelPaypal(r.Context(), env, user)
				if err != nil {
					log.Printf("unable to get cancel Paypal subscription: %s", err)
					w.WriteHeader(500)
					return
				}
			}
		}

		err = kc.UpdateUserStripeInfo(r.Context(), user, customer, sub)
		if err != nil {
			log.Printf("error while updating Keycloak for Stripe subscription webhook event: %s", err)
			w.WriteHeader(500)
			return
		}
	}
}

func newAdminDumpHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := kc.ListUsers(r.Context())
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

func cancelPaypal(ctx context.Context, env *conf.Env, user *keycloak.User) error {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.paypal.com/v1/billing/subscriptions/%s", user.PaypalSubscriptionID), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(env.PaypalClientID, env.PaypalClientSecret)

	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()

	if getResp.StatusCode == 404 {
		log.Printf("not canceling paypal subscription because it doesn't exist: %s", user.PaypalSubscriptionID)
		return nil
	}
	if getResp.StatusCode > 299 {
		return fmt.Errorf("non-200 response from Paypal when getting subscription: %d", getResp.StatusCode)
	}

	current := struct {
		Status string `json:"status"`
	}{}
	err = json.NewDecoder(getResp.Body).Decode(&current)
	if err != nil {
		return err
	}
	if current.Status == "CANCELLED" {
		log.Printf("not canceling paypal subscription because it's already canceled: %s", user.PaypalSubscriptionID)
		return nil
	}

	body := bytes.NewBufferString(`{ "reason": "migrated account" }`)
	req, err = http.NewRequest("POST", fmt.Sprintf("https://api.paypal.com/v1/billing/subscriptions/%s/cancel", user.PaypalSubscriptionID), body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(env.PaypalClientID, env.PaypalClientSecret)
	req.Header.Set("Content-Type", "application/json")

	postResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()

	if postResp.StatusCode == 404 {
		log.Printf("not canceling paypal subscription because it doesn't exist even after previous check: %s", user.PaypalSubscriptionID)
		return nil
	}
	if postResp.StatusCode > 299 {
		body, _ := io.ReadAll(postResp.Body)
		return fmt.Errorf("non-200 response from Paypal when canceling: %d - %s", postResp.StatusCode, body)
	}

	log.Printf("canceled paypal subscription: %s", user.PaypalSubscriptionID)
	reporting.DefaultSink.Publish(user.Email, "CanceledPaypal", "Successfully migrated user off of paypal")
	return nil
}
