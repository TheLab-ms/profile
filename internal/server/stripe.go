package server

import (
	"net/http"
	"strconv"

	"github.com/stripe/stripe-go/v75"
	billingsession "github.com/stripe/stripe-go/v75/billingportal/session"
	"github.com/stripe/stripe-go/v75/checkout/session"

	"github.com/TheLab-ms/profile/internal/payment"
	"github.com/TheLab-ms/profile/internal/reporting"
)

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
		checkoutParams.LineItems = payment.CalculateLineItems(user, priceID, s.PriceCache)
		checkoutParams.Discounts = payment.CalculateDiscount(user, priceID, s.PriceCache)
		if checkoutParams.Discounts == nil {
			// Stripe API doesn't allow Discounts and AllowPromotionCodes to be set
			checkoutParams.AllowPromotionCodes = stripe.Bool(true)
		}

		checkoutParams.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata:           map[string]string{"etag": etag},
			BillingCycleAnchor: payment.CalculateBillingCycleAnchor(user), // This enables migration from paypal
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
