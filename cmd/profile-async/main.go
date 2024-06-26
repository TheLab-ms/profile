package main

import (
	"context"
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

func main() {
	ctx := context.TODO()
	env := &conf.Env{}
	env.MustLoad()

	discordSyncUsers := flowcontrol.NewQueue[int64]()
	go discordSyncUsers.Run(ctx)
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
