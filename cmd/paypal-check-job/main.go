package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/paypal"
	"github.com/TheLab-ms/profile/internal/reporting"
	"golang.org/x/time/rate"
)

func main() {
	if err := run(); err != nil {
		log.Printf("terminal error: %s", err)
		os.Exit(1)
	}
}

func run() error {
	env := &conf.Env{}
	env.MustLoad()

	kc := keycloak.New[*datamodel.User](env)
	ppc := paypal.NewClient(env)
	ctx := context.Background()

	users, err := kc.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	reporting.DefaultSink, err = reporting.NewSink(env, kc)
	if err != nil {
		return err
	}
	kc.Sink = reporting.DefaultSink

	limiter := rate.NewLimiter(rate.Every(time.Millisecond*500), 1)
	for _, extended := range users {
		user := extended.User
		if !extended.ActiveMember || user.PaypalMetadata.TransactionID == "" || user.StripeCustomerID != "" {
			continue
		}
		limiter.Wait(ctx)

		current, err := ppc.GetSubscription(ctx, user.PaypalMetadata.TransactionID)
		if err != nil {
			log.Printf("error while getting paypal subscription for member %s: %s", user.Email, err)
			continue
		}
		if current == nil {
			log.Printf("no subscription found for id %s", user.PaypalMetadata.TransactionID)
			continue
		}
		active := current.Status != "CANCELLED"
		price, _ := strconv.ParseFloat(current.Billing.LastPayment.Amount.Value, 64)

		log.Printf("paypal subscription %s is in state active=%t for member %s who last visited on %s", user.PaypalMetadata.TransactionID, active, user.Email, user.LastSwipeTime.Format("2006-01-02"))
		if !active {
			err = kc.Deactivate(ctx, user)
			if err != nil {
				log.Printf("error while deactivating user: %s", err)
				continue
			}
			reporting.DefaultSink.Eventf(user.Email, "PayPalSubscriptionCanceled", "We observed the member's PayPal status in an inactive state")
			continue
		}

		if price == user.PaypalMetadata.Price && current.Billing.LastPayment.Time == user.PaypalMetadata.TimeRFC3339 {
			continue
		}

		user.PaypalMetadata.TimeRFC3339 = current.Billing.LastPayment.Time
		user.PaypalMetadata.Price = price
		err = kc.WriteUser(ctx, user)
		if err != nil {
			log.Printf("error while updating user Paypal metadata: %s", err)
			continue
		}
		log.Printf("updated paypal metadata for member: %s", user.Email)
	}

	log.Printf("done!")
	time.Sleep(time.Second) // let the events get flushed to the db (this is v dumb)
	return nil
}
