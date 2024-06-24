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
)

func syncKeycloak(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], userID string) error {
	_, err := kc.GetUser(ctx, userID)
	if errors.Is(err, keycloak.ErrNotFound) {
		return nil // ignore any users that have been deleted since being enqueued
	}
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}

	return nil
}

func syncDiscord(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], bot *chatbot.Bot, userID int64) error {
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
		if err != nil {
			return fmt.Errorf("extending user: %w", err)
		}
		status.ActiveMember = extended.ActiveMember
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

	discordUsers := flowcontrol.NewQueue[int64]()
	go discordUsers.Run(ctx)
	keycloakUsers := flowcontrol.NewQueue[string]()
	go keycloakUsers.Run(ctx)

	kc := keycloak.New[*datamodel.User](env)

	var err error
	reporting.DefaultSink, err = reporting.NewSink(env, kc)
	if err != nil {
		log.Fatal(err)
	}

	bot, err := chatbot.NewBot(env)
	if err != nil {
		log.Fatal(err)
	}

	// Webhook registration
	if env.KeycloakRegisterWebhook {
		err = kc.EnsureWebhook(ctx, fmt.Sprintf("%s/webhooks/keycloak", env.SelfURL))
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
					discordUsers.Add(user.DiscordUserID)
				}
				keycloakUsers.Add(user.UUID)
			}
			return true
		}),
	}).Run(ctx)

	// Discord resync loop
	go (&flowcontrol.Loop{
		Handler: flowcontrol.RetryHandler(time.Hour*24, func(ctx context.Context) bool {
			log.Printf("resyncing discord users...")
			err := bot.ListUsers(ctx, func(id int64) {
				discordUsers.Add(id)
			})
			if err != nil {
				log.Printf("error while listing discord users for resync: %s", err)
				return false
			}
			return true
		}),
	}).Run(ctx)

	// Workers pull messages off of the queue and process them
	go flowcontrol.RunWorker(ctx, discordUsers, func(id int64) error {
		return syncDiscord(ctx, kc, bot, id)
	})
	go flowcontrol.RunWorker(ctx, keycloakUsers, func(id string) error {
		return syncKeycloak(ctx, kc, id)
	})

	// Webhook server
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.Handle("/webhooks/keycloak", keycloak.NewWebhookHandler(func(userID string) bool {
		log.Printf("got keycloak webhook for user %s", userID)
		keycloakUsers.Add(userID)

		user, err := kc.GetUser(ctx, userID)
		if err != nil {
			log.Printf("error while getting keycloak user: %s", err)
			return false
		}
		discordUsers.Add(user.DiscordUserID)
		return true
	}))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
