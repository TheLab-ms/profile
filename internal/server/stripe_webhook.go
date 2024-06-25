package server

import (
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v78"
	"github.com/stripe/stripe-go/v78/customer"
	"github.com/stripe/stripe-go/v78/subscription"
	"github.com/stripe/stripe-go/v78/webhook"

	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/TheLab-ms/profile/internal/reporting"
)

func (s *Server) newStripeWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error while reading Stripe webhook body: %s", err)
			w.WriteHeader(503)
			return
		}

		event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), s.Env.StripeWebhookKey)
		if err != nil {
			log.Printf("error while constructing Stripe webhook event: %s", err)
			w.WriteHeader(400)
			return
		}

		if strings.HasPrefix(string(event.Type), "price.") || strings.HasPrefix(string(event.Type), "coupon.") {
			log.Printf("refreshing Stripe caches because a webhook was received that suggests things have changed")
			s.PriceCache.Kick()
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

		subID := event.Data.Object["id"].(string)
		sub, err := subscription.Get(subID, &stripe.SubscriptionParams{})
		if err != nil {
			log.Printf("unable to get Stripe subscription object: %s", err)
			w.WriteHeader(500)
			return
		}

		customer, err := customer.Get(sub.Customer.ID, &stripe.CustomerParams{})
		if err != nil {
			log.Printf("unable to get Stripe customer object: %s", err)
			w.WriteHeader(500)
			return
		}
		log.Printf("got Stripe subscription event for member %q, state=%s", customer.Email, sub.Status)

		user, err := s.Keycloak.GetUserByEmail(r.Context(), customer.Email)
		if err != nil {
			log.Printf("unable to get user by email address: %s", err)
			w.WriteHeader(500)
			return
		}

		// Clean up old paypal sub if it still exists
		if s.Env.PaypalClientID != "" && s.Env.PaypalClientSecret != "" && user.PaypalMetadata.TransactionID != "" {
			err := s.Paypal.Cancel(r.Context(), user)
			if err != nil {
				log.Printf("unable to get cancel Paypal subscription: %s", err)
				w.WriteHeader(500)
				return
			}
		}

		// No more paypal since they're in Stripe!
		user.PaypalMetadata = datamodel.PaypalMetadata{}

		active := sub.Status == stripe.SubscriptionStatusActive || sub.Status == stripe.SubscriptionStatusTrialing
		if active {
			user.StripeCustomerID = customer.ID
			user.StripeSubscriptionID = sub.ID
			user.StripeCancelationTime = time.Unix(sub.CancelAt, 0)

			if customer.ID != user.StripeCustomerID || sub.ID != user.StripeSubscriptionID {
				reporting.DefaultSink.Eventf(user.Email, "StripeSubscriptionChanged", "A Stripe webhook caused the user's Stripe customer and/or subscription to change")
			} else if !user.StripeCancelationTime.After(time.Unix(0, 0)) && sub.CancelAt > 0 {
				reporting.DefaultSink.Eventf(user.Email, "StripeSubscriptionCanceled", "The user canceled their subscription")
			}
		} else {
			// Revoke building access when payment is canceled
			// TODO: Does this include the period between cancelation and expiration?
			user.BuildingAccessApprover = ""
			user.StripeSubscriptionID = ""
		}

		err = s.Keycloak.WriteUser(r.Context(), user)
		if err != nil {
			log.Printf("error while updating Keycloak for Stripe subscription webhook event: %s", err)
			w.WriteHeader(500)
			return
		}

		err = s.Keycloak.UpdateGroupMembership(r.Context(), user, active)
		if err != nil {
			log.Printf("error while updating Keycloak group membership for Stripe subscription webhook event: %s", err)
			w.WriteHeader(500)
			return
		}
	}
}
