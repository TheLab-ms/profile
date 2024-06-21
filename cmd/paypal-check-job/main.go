package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/keycloak"
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

	kc := keycloak.New(env)
	ctx := context.Background()

	users, err := kc.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	reporting.DefaultSink, err = reporting.NewSink(env)
	if err != nil {
		return err
	}

	limiter := rate.NewLimiter(rate.Every(time.Millisecond*500), 1)
	for _, user := range users {
		if !user.ActiveMember || user.PaypalMetadata.TransactionID == "" || user.StripeCustomerID != "" {
			continue
		}
		limiter.Wait(ctx)

		current, err := getSubscriptionMetadata(ctx, env, user.PaypalMetadata.TransactionID)
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
			err = kc.Deactivate(ctx, user.User)
			if err != nil {
				log.Printf("error while deactivating user: %s", err)
			}
			continue
		}

		if price == user.PaypalMetadata.Price && current.Billing.LastPayment.Time == user.PaypalMetadata.TimeRFC3339 {
			continue
		}

		user.User.PaypalMetadata.TimeRFC3339 = current.Billing.LastPayment.Time
		user.User.PaypalMetadata.Price = price
		err = kc.WriteUser(ctx, user.User)
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

func getSubscriptionMetadata(ctx context.Context, env *conf.Env, id string) (*paypalSubscription, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.paypal.com/v1/billing/subscriptions/%s", id), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(env.PaypalClientID, env.PaypalClientSecret)

	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer getResp.Body.Close()

	if getResp.StatusCode == 404 {
		return nil, nil
	}
	if getResp.StatusCode > 299 {
		body, _ := io.ReadAll(getResp.Body)
		return nil, fmt.Errorf("error response %d from Paypal when getting subscription: %s", getResp.StatusCode, body)
	}

	current := &paypalSubscription{}
	err = json.NewDecoder(getResp.Body).Decode(&current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

type paypalSubscription struct {
	Status  string            `json:"status"`
	Billing paypalBillingInfo `json:"billing_info"`
}

type paypalBillingInfo struct {
	LastPayment paypalPayment `json:"last_payment"`
}

type paypalPayment struct {
	Amount paypalAmount `json:"amount"`
	Time   time.Time    `json:"time"`
}

type paypalAmount struct {
	Value string `json:"value"`
}
