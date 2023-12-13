package main

import (
	"bytes"
	"context"
	"embed"
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
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
	"github.com/stripe/stripe-go/v75"
	"github.com/stripe/stripe-go/v75/checkout/session"
	"github.com/stripe/stripe-go/v75/customer"
	"github.com/stripe/stripe-go/v75/subscription"
	"github.com/stripe/stripe-go/v75/webhook"
	"golang.org/x/time/rate"

	"github.com/TheLab-ms/profile/conf"
	"github.com/TheLab-ms/profile/keycloak"
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
	http.HandleFunc("/profile/cancel", newCancelHandler(env, kc))

	// Webhooks
	http.HandleFunc("/webhooks/docuseal", newDocusealWebhookHandler(kc))
	http.HandleFunc("/webhooks/stripe", newStripeWebhookHandler(env, kc))

	// Embed (into the compiled binary) and serve any files from the assets directory
	http.Handle("/assets/", http.FileServer(http.FS(assets)))

	// Various leadership-only admin endpoints
	http.HandleFunc("/admin/dump", onlyLeadership(newAdminDumpHandler(kc)))
	http.HandleFunc("/admin/refresh", onlyLeadership(newAdminRefreshHandler(priceCache)))

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

		templates.ExecuteTemplate(w, "signup.html", viewData)
	}
}

func newProfileViewHandler(kc *keycloak.Keycloak, pc *stripeutil.PriceCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := kc.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while fetching user: %s", err)
			return
		}

		etag := r.URL.Query().Get("etag")
		viewData := map[string]any{
			"page":            "profile",
			"user":            user,
			"prices":          pc.GetPrices(),
			"migratedAccount": user.LastPaypalTransactionTime != time.Time{},
			"stripePending":   etag != "" && etag != user.StripeETag,
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
		if fobIdStr != "" && err != nil {
			http.Error(w, "invalid fobid", 400)
			return
		}

		if fobIdStr != "" {
			conflict, err := kc.BadgeIDInUse(r.Context(), fobID)
			if err != nil {
				renderSystemError(w, "error while checking if badge ID is in use: %s", err)
				return
			}
			if conflict {
				http.Error(w, "that badge ID is already in use", 400)
				return
			}
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
		if len(first) > 256 || len(last) > 256 {
			http.Error(w, "name is too long", 400)
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

func newStripeCheckoutHandler(env *conf.Env, kc *keycloak.Keycloak, pc *stripeutil.PriceCache) http.HandlerFunc {
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
			priceID := r.URL.Query().Get("price")
			checkoutParams.Mode = stripe.String(string(stripe.CheckoutSessionModeSubscription))
			checkoutParams.AllowPromotionCodes = stripe.Bool(true)
			checkoutParams.Discounts = calculateDiscount(user, priceID, pc)
			checkoutParams.LineItems = calculateLineItems(user, priceID, pc)
			checkoutParams.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{
				Metadata:           map[string]string{"etag": etag},
				BillingCycleAnchor: calculateBillingCycleAnchor(user),
			}
			if checkoutParams.SubscriptionData.BillingCycleAnchor != nil {
				checkoutParams.SubscriptionData.ProrationBehavior = stripe.String("none")
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

		// Clean up old paypal sub if it still exists
		if env.PaypalClientID != "" && env.PaypalClientSecret != "" {
			user, err := kc.GetUserByEmail(r.Context(), customer.Email)
			if err != nil {
				log.Printf("unable to get user by email address: %s", err)
				w.WriteHeader(500)
				return
			}

			if user.PaypalSubscriptionID != "" { // this is removed by UpdateUserStripeInfo
				err := cancelPaypal(r.Context(), env, user)
				if err != nil {
					log.Printf("unable to get cancel Paypal subscription: %s", err)
					w.WriteHeader(500)
					return
				}
			}
		}

		// TODO: Handle user 404s to avoid constantly failing webhooks?
		err = kc.UpdateUserStripeInfo(r.Context(), customer, sub)
		if err != nil {
			log.Printf("error while updating Keycloak for Stripe subscription webhook event: %s", err)
			w.WriteHeader(500)
			return
		}
	}
}

func newAdminDumpHandler(kc *keycloak.Keycloak) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kc.DumpUsers(r.Context(), w)
	}
}

func newAdminRefreshHandler(pc *stripeutil.PriceCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("explicitly refreshing caches")
		pc.Refresh()

		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "accepted\n")
		w.WriteHeader(202)
	}
}

func onlyLeadership(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("X-Forwarded-Groups"), "leadership") {
			http.Error(w, "unauthorized", 403)
			return
		}
		next(w, r)
	}
}

func calculateLineItems(user *keycloak.User, priceID string, pc *stripeutil.PriceCache) []*stripe.CheckoutSessionLineItemParams {
	// Migrate existing paypal users at their current rate
	if priceID == "paypal" {
		interval := "month"
		if user.LastPaypalTransactionPrice > 50 {
			interval = "year"
		}

		cents := user.LastPaypalTransactionPrice * 100
		productID := pc.GetPrices()[0].ProductID // all prices reference the same product
		return []*stripe.CheckoutSessionLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:          stripe.String("usd"),
				Product:           &productID,
				UnitAmountDecimal: &cents,
				Recurring: &stripe.CheckoutSessionLineItemPriceDataRecurringParams{
					Interval: &interval,
				},
			},
		}}
	}

	return []*stripe.CheckoutSessionLineItemParams{{
		Price:    stripe.String(priceID),
		Quantity: stripe.Int64(1),
	}}
}

func calculateDiscount(user *keycloak.User, priceID string, pc *stripeutil.PriceCache) []*stripe.CheckoutSessionDiscountParams {
	if user.DiscountType == "" || priceID == "" {
		return nil
	}
	for _, price := range pc.GetPrices() {
		if price.ID == priceID && price.CouponsByDiscountType != nil && price.CouponsByDiscountType[user.DiscountType] != "" {
			return []*stripe.CheckoutSessionDiscountParams{{
				Coupon: stripe.String(price.CouponsByDiscountType[user.DiscountType]),
			}}
		}
	}
	return nil
}

func calculateBillingCycleAnchor(user *keycloak.User) *int64 {
	if user.LastPaypalTransactionPrice == 0 {
		return nil
	}

	var end time.Time
	if user.LastPaypalTransactionPrice > 41 {
		// Annual
		end = user.LastPaypalTransactionTime.Add(time.Hour * 24 * 365)
	} else {
		// Monthly
		end = user.LastPaypalTransactionTime.Add(time.Hour * 24 * 30)
	}

	// Stripe will throw an error if the cycle anchor is before the current time
	if time.Until(end) < time.Minute {
		return nil
	}

	ts := end.Unix()
	return &ts
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

	postResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()
	postResp.Header.Set("Content-Type", "application/json")

	if postResp.StatusCode == 404 {
		log.Printf("not canceling paypal subscription because it doesn't exist even after previous check: %s", user.PaypalSubscriptionID)
		return nil
	}
	if postResp.StatusCode > 299 {
		body, _ := io.ReadAll(postResp.Body)
		return fmt.Errorf("non-200 response from Paypal when canceling: %d - %s", postResp.StatusCode, body)
	}

	log.Printf("canceled paypal subscription: %s", user.PaypalSubscriptionID)
	return nil
}
