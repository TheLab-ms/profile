package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/TheLab-ms/profile/internal/chatbot"
	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/TheLab-ms/profile/internal/flowcontrol"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/reporting"
	"golang.org/x/time/rate"
)

var signupEmailLimiter = rate.NewLimiter(rate.Every(time.Second*10), 1)

func handleUserSignupEmail(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], userID string) error {
	user, err := kc.GetUser(ctx, userID)
	if errors.Is(keycloak.ErrNotFound, err) {
		return nil // ignore any users that have been deleted since being enqueued
	}
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}

	// Send emails at least once while avoiding duplicates when possible
	if user.SignupEmailSentTime.After(time.Unix(0, 0)) {
		return nil
	}

	// Don't send email if we somehow didn't become aware of the user until >24hr after signup time.
	if time.Since(user.SignupTime) > time.Hour*24 {
		return nil
	}

	signupEmailLimiter.Wait(ctx)
	err = kc.SendSignupEmail(ctx, user.UUID)
	if err != nil {
		return err
	}
	reporting.DefaultSink.Eventf(user.Email, "SignupEmailSent", "sent initial password reset email to new user")

	user.SignupEmailSentTime = time.Now()
	return kc.WriteUser(ctx, user)
}

func handleDiscordSync(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], bot *chatbot.Bot, userID int64) error {
	user, err := kc.GetUserByAttribute(ctx, "discordUserID", strconv.FormatInt(userID, 10))
	if errors.Is(keycloak.ErrNotFound, err) {
		// ignore 404s since we may still need to clean up the role
		err = nil
	}
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}

	status := &chatbot.UserStatus{ID: userID}
	if user != nil {
		status.Email = user.Email
		extended, err := kc.ExtendUser(ctx, user, user.UUID)
		if errors.Is(keycloak.ErrNotFound, err) {
			// ignore 404s since we may still need to clean up the role
			err = nil
		}
		if err != nil {
			return fmt.Errorf("extending user: %w", err)
		}
		if extended != nil {
			status.ActiveMember = extended.ActiveMember
		}
	}

	err = bot.SyncUser(ctx, status)
	if err != nil {
		return fmt.Errorf("syncing discord user: %w", err)
	}

	return nil
}

func handleConwaySync(ctx context.Context, env *conf.Env, kc *keycloak.Keycloak[*datamodel.User], userID string) error {
	user, err := kc.GetUser(ctx, userID)
	if errors.Is(keycloak.ErrNotFound, err) {
		return nil // we don't need to clean up
	}
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}

	ext, err := kc.ExtendUser(ctx, user, userID)
	if err != nil {
		return fmt.Errorf("extending user: %w", err)
	}

	out := map[string]any{
		"email":     user.Email,
		"created":   user.CreationTime / 1000,
		"name":      fmt.Sprintf("%s %s", user.First, user.Last),
		"confirmed": true,
		// "leadership":                false, // remember to just set this manually
		"non_billable":              user.NonBillable,
		"price_tier":                user.DiscountType,
		"price_amount":              40, // overridden below
		"building_access_approver":  user.BuildingAccessApprover,
		"fob_id":                    user.FobID,
		"stripe_customer_id":        user.StripeCustomerID,
		"stripe_subscription_id":    user.StripeSubscriptionID,
		"stripe_subscription_state": nil,
		"stripe_cancelation_time":   nil,
		"paypal_subscription_id":    user.PaypalMetadata.TransactionID,
		"paypal_price":              user.PaypalMetadata.Price,
		"paypal_last_payment":       nil,
		"waiver_signed":             true,
	}
	if out["fob_id"] == 0 {
		out["fob_id"] = nil
	}
	if out["stripe_customer_id"] == "" {
		out["stripe_customer_id"] = nil
	}
	if out["stripe_subscription_id"] == "" {
		out["stripe_subscription_id"] = nil
	}
	if out["paypal_subscription_id"] == "" {
		out["paypal_subscription_id"] = nil
	}
	if out["paypal_price"] == float64(0) {
		out["paypal_price"] = nil
	}
	if user.PaypalMetadata.TimeRFC3339.After(time.Unix(0, 0)) {
		out["paypal_last_payment"] = user.PaypalMetadata.TimeRFC3339.Unix()
	}
	if user.StripeCancelationTime.After(time.Unix(0, 0)) {
		out["stripe_cancelation_time"] = user.StripeCancelationTime.Unix()
	}
	if ext.ActiveMember {
		out["stripe_subscription_state"] = "active"
	}
	if user.DiscountType != "" {
		out["price_amount"] = 20
	}
	if user.DiscountType == "family" {
		out["price_amount"] = 15
	}

	if env.ConwayURL == "" || env.ConwayToken == "" {
		log.Printf("Conway URL or Token not set")
		return nil
	}

	js, err := json.Marshal(&out)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", fmt.Sprintf("%s/api/members/%s", env.ConwayURL, user.Email), bytes.NewBuffer(js))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", env.ConwayToken))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

func main() {
	ctx := context.TODO()
	env := &conf.Env{}
	env.MustLoad()

	discordSyncUsers := flowcontrol.NewQueue[int64]()
	go discordSyncUsers.Run(ctx)
	conwaySyncUsers := flowcontrol.NewQueue[string]()
	go conwaySyncUsers.Run(ctx)
	signupEmailUsers := flowcontrol.NewQueue[string]()
	go signupEmailUsers.Run(ctx)

	kc := keycloak.New[*datamodel.User](env)

	var err error
	reporting.DefaultSink, err = reporting.NewSink(env, kc)
	if err != nil {
		log.Fatal(err)
	}
	kc.Sink = reporting.DefaultSink

	bot, err := chatbot.NewBot(env)
	if err != nil {
		log.Fatal(err)
	}

	// Webhook registration
	if env.KeycloakRegisterWebhook {
		err = kc.EnsureWebhook(ctx, fmt.Sprintf("%s/webhooks/keycloak", env.WebhookURL))
		if err != nil {
			log.Fatal(err)
		}
	}

	// Keycloak resync loop
	go (&flowcontrol.Loop{
		Handler: flowcontrol.RetryHandler(time.Hour*2, func(ctx context.Context) bool {
			log.Printf("resyncing keycloak users...")
			users, err := kc.ListUsers(ctx)
			if err != nil {
				log.Printf("error while listing members for resync: %s", err)
				return false
			}
			for _, extended := range users {
				user := extended.User
				if user.DiscordUserID > 0 {
					discordSyncUsers.Add(user.DiscordUserID)
				}
				signupEmailUsers.Add(user.UUID)
				conwaySyncUsers.Add(user.UUID)
			}
			return true
		}),
	}).Run(ctx)

	// Discord resync loop
	go (&flowcontrol.Loop{
		Handler: flowcontrol.RetryHandler(time.Hour*24, func(ctx context.Context) bool {
			log.Printf("resyncing discord users...")
			err := bot.ListUsers(ctx, func(id int64) {
				discordSyncUsers.Add(id)
			})
			if err != nil {
				log.Printf("error while listing discord users for resync: %s", err)
				return false
			}
			return true
		}),
	}).Run(ctx)

	// Workers pull messages off of the queue and process them
	go flowcontrol.RunWorker(ctx, discordSyncUsers, func(id int64) error {
		return handleDiscordSync(ctx, kc, bot, id)
	})
	go flowcontrol.RunWorker(ctx, signupEmailUsers, func(id string) error {
		return handleUserSignupEmail(ctx, kc, id)
	})
	go flowcontrol.RunWorker(ctx, conwaySyncUsers, func(id string) error {
		defer time.Sleep(time.Millisecond * 50) // throttling lol
		return handleConwaySync(ctx, env, kc, id)
	})

	// Webhook server
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.Handle("/webhooks/keycloak", keycloak.NewWebhookHandler(func(userID string) bool {
		log.Printf("got keycloak webhook for user %s", userID)
		signupEmailUsers.Add(userID)

		user, err := kc.GetUser(ctx, userID)
		if err != nil {
			log.Printf("error while getting keycloak user: %s", err)
			return false
		}
		discordSyncUsers.Add(user.DiscordUserID)
		return true
	}))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
