package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

	kc := keycloak.New(env, nil)
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
		if !user.ActiveMember || user.PaypalSubscriptionID == "" || user.StripeCustomerID != "" {
			continue
		}
		limiter.Wait(ctx)

		active, err := getSubscriptionMetadata(ctx, env, user.PaypalSubscriptionID)
		if err != nil {
			log.Printf("error while getting paypal subscription for member %s: %s", user.Email, err)
			continue
		}
		log.Printf("paypal subscription %s is in state active=%t for member %s who last visited on %s", user.PaypalSubscriptionID, active, user.Email, user.LastSwipeTime.Format("2006-01-02"))
		if active {
			continue
		}

		err = kc.Deactivate(ctx, user.User)
		if err != nil {
			log.Printf("error while deactivating user: %s", err)
		}
	}

	log.Printf("done!")
	time.Sleep(time.Second) // let the events get flushed to the db (this is v dumb)
	return nil
}

func getSubscriptionMetadata(ctx context.Context, env *conf.Env, id string) (bool, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.paypal.com/v1/billing/subscriptions/%s", id), nil)
	if err != nil {
		return false, err
	}
	req.SetBasicAuth(env.PaypalClientID, env.PaypalClientSecret)

	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer getResp.Body.Close()

	if getResp.StatusCode == 404 {
		return false, nil
	}
	if getResp.StatusCode > 299 {
		body, _ := io.ReadAll(getResp.Body)
		return false, fmt.Errorf("error response %d from Paypal when getting subscription: %s", getResp.StatusCode, body)
	}

	current := struct {
		Status string `json:"status"`
	}{}
	err = json.NewDecoder(getResp.Body).Decode(&current)
	if err != nil {
		return false, err
	}
	return current.Status != "CANCELLED", nil
}
