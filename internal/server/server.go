package server

import (
	"bytes"
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

	"github.com/stripe/stripe-go/v75"
	billingsession "github.com/stripe/stripe-go/v75/billingportal/session"
	"github.com/stripe/stripe-go/v75/checkout/session"
	"github.com/teambition/rrule-go"
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
	mux.HandleFunc("/webhooks/docuseal", s.newDocusealWebhookHandler())
	mux.HandleFunc("/webhooks/stripe", payment.NewWebhookHandler(s.Env, s.Keycloak, s.PriceCache))
	mux.Handle("/assets/", http.FileServer(http.FS(s.Assets)))
	mux.HandleFunc("/admin/dump", onlyLeadership(s.newAdminDumpHandler()))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/api/events", s.newListEventsHandler())
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

func (s *Server) newStripeCheckoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user from Keycloak: %s", err)
			return
		}
		etag := strconv.FormatInt(user.StripeETag+1, 10)

		// If there is an active payment on record for this user, start a session to manage the subscription.
		if user.ActiveMember {
			sessionParams := &stripe.BillingPortalSessionParams{
				Customer:  stripe.String(user.StripeCustomerID),
				ReturnURL: stripe.String(s.Env.SelfURL + "/profile"),
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
			SuccessURL:    stripe.String(s.Env.SelfURL + "/profile?i=" + etag),
			CancelURL:     stripe.String(s.Env.SelfURL + "/profile"),
		}
		checkoutParams.Context = r.Context()

		// Calculate specific pricing based on the member's profile
		priceID := r.URL.Query().Get("price")
		checkoutParams.LineItems = pricing.CalculateLineItems(user, priceID, s.PriceCache)
		checkoutParams.Discounts = pricing.CalculateDiscount(user, priceID, s.PriceCache)
		if checkoutParams.Discounts == nil {
			// Stripe API doesn't allow Discounts and AllowPromotionCodes to be set
			checkoutParams.AllowPromotionCodes = stripe.Bool(true)
		}

		checkoutParams.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata:           map[string]string{"etag": etag},
			BillingCycleAnchor: pricing.CalculateBillingCycleAnchor(user), // This enables migration from paypal
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

func (s *Server) newDocusealRedirectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.Keycloak.GetUser(r.Context(), getUserID(r))
		if err != nil {
			renderSystemError(w, "error while getting user: %s", err)
			return
		}

		by, _ := json.Marshal(map[string]any{"template_id": 1, "emails": user.Email})
		req, err := http.NewRequest("POST", s.Env.DocusealURL+"/api/submissions", bytes.NewBuffer(by))
		if err != nil {
			renderSystemError(w, "error while creating docuseal submission request: %s", err)
			return
		}
		req.Header.Add("X-Auth-Token", s.Env.DocusealToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			renderSystemError(w, "error sending docuseal submission request: %s", err)
			return
		}
		defer resp.Body.Close()

		subs := []struct {
			Slug string `json:"slug"`
		}{}
		err = json.NewDecoder(resp.Body).Decode(&subs)
		if err != nil {
			renderSystemError(w, "error while decoding docuseal submission: %s", err)
			return
		}
		if len(subs) == 0 {
			renderSystemError(w, "no submissions were returned from docuseal: %s", err)
			return
		}

		log.Printf("initiated docuseal submission %q for user %s", subs[0].Slug, user.Email)
		reporting.DefaultSink.Publish(user.Email, "DocusealSubmissionCreated", "created docuseal submission: %s", subs[0].Slug)
		http.Redirect(w, r, s.Env.DocusealURL+"/s/"+subs[0].Slug, http.StatusTemporaryRedirect)
	}
}

func (s *Server) newDocusealWebhookHandler() http.HandlerFunc {
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

		user, err := s.Keycloak.GetUserByEmail(r.Context(), body.Data.Email)
		if err != nil {
			log.Printf("unable to get user by email address: %s", err)
			w.WriteHeader(500)
			return
		}

		err = s.Keycloak.UpdateUserWaiverState(r.Context(), user)
		if err != nil {
			log.Printf("error while updating user's waiver state: %s", err)
			w.WriteHeader(500)
			return
		}

		reporting.DefaultSink.Publish(body.Data.Email, "SignedWaiver", "user signed waiver")
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

func (s *Server) newListEventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		events := s.EventsCache.GetEvents()

		// Expand the recurrence of every event
		var expanded []*eventPublic
		for _, event := range events {
			// Support a magic location string to designate members only events
			membersOnly := event.Metadata.Location == "TheLab (Members Only)"

			if event.Recurrence == nil {
				expanded = append(expanded, &eventPublic{
					Name:        event.Name,
					Description: event.Description,
					Start:       event.Start.UTC().Unix(),
					End:         event.End.UTC().Unix(),
					MembersOnly: membersOnly,
				})
				continue
			}

			// Expand out the recurring events into a slice of start times
			ropts := rrule.ROption{
				Freq:     rrule.Frequency(event.Recurrence.Freq),
				Interval: event.Recurrence.Interval,
				Dtstart:  event.Recurrence.Start.UTC(),
				Bymonth:  event.Recurrence.ByMonth,
			}
			for _, day := range event.Recurrence.ByWeekday {
				// annoying that the library doesn't expose days of the week as ints - they line up with discord's representation anyway
				switch day {
				case 0:
					ropts.Byweekday = append(ropts.Byweekday, rrule.MO)
				case 1:
					ropts.Byweekday = append(ropts.Byweekday, rrule.TU)
				case 2:
					ropts.Byweekday = append(ropts.Byweekday, rrule.WE)
				case 3:
					ropts.Byweekday = append(ropts.Byweekday, rrule.TH)
				case 4:
					ropts.Byweekday = append(ropts.Byweekday, rrule.FR)
				case 5:
					ropts.Byweekday = append(ropts.Byweekday, rrule.SA)
				case 6:
					ropts.Byweekday = append(ropts.Byweekday, rrule.SU)
				}
			}
			rule, err := rrule.NewRRule(ropts)
			if err != nil {
				renderSystemError(w, "error expanding recurrence: %s", err.Error())
				return
			}

			// Our expansion ends either 1mo out from now or when the recurrence ends
			var end time.Time
			if event.Recurrence.End != nil {
				end = *event.Recurrence.End
			} else {
				end = now.Add(time.Hour * 24 * 30)
			}

			// Calculate the end time by adding the duration of the event to the start time
			times := rule.Between(now, end, true)
			duration := event.End.Sub(event.Start)
			for _, start := range times {
				expanded = append(expanded, &eventPublic{
					Name:        event.Name,
					Description: event.Description,
					Start:       start.UTC().Unix(),
					End:         start.Add(duration).Unix(),
					MembersOnly: membersOnly,
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		json.NewEncoder(w).Encode(&expanded)
	}
}

type eventPublic struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	MembersOnly bool   `json:"membersOnly"`
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
