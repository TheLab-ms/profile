package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/TheLab-ms/profile/internal/chatbot"
	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/reporting"
)

func main() {
	env := &conf.Env{}
	env.MustLoad()

	var err error
	reporting.DefaultSink, err = reporting.NewSink(env)
	if err != nil {
		log.Fatal(err)
	}

	q := NewQueue()
	kc := keycloak.New(env)
	bot, err := chatbot.NewBot(env)
	if err != nil {
		log.Fatal(err)
	}

	// Webhook registration
	if env.KeycloakRegisterWebhook {
		err = kc.EnsureWebhook(context.TODO(), fmt.Sprintf("%s/webhooks/keycloak", env.SelfURL))
		if err != nil {
			log.Fatal(err)
		}
	}

	// Resync loop
	go func() {
		ticker := time.NewTicker(time.Hour)
		for range ticker.C {
			ids, err := kc.ListUserIDs(context.TODO())
			if err != nil {
				log.Printf("error while listing members for resync: %s", err)
				continue
			}
			for _, id := range ids {
				q.Add(id)
			}
		}
	}()

	// Message processor loop
	go func() {
		for {
			item := q.Get()
			start := time.Now()
			log.Printf("syncing user %s", item)
			err := syncUser(context.TODO(), kc, bot, item)
			if err == nil {
				q.Done(item)
				log.Printf("sync'd user %s in %s", item, time.Since(start))
				continue
			}
			log.Printf("error while syncing user %q: %s", item, err)
			q.Retry(item)
		}
	}()

	// Webhook server
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("/webhooks/keycloak", func(w http.ResponseWriter, r *http.Request) {
		msg := &webhookMsg{}
		err := json.NewDecoder(r.Body).Decode(msg)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if msg.ResourceType != "USER" {
			return
		}
		q.Add(msg.Details.UserID)
		log.Printf("got keycloak webhook for user %s", msg.Details.UserID)
	})

	log.Fatal(http.ListenAndServe(":8080", mux))
}

type webhookMsg struct {
	ResourceType string `json:"resourceType"` // e.g. == "USER"
	Details      struct {
		UserID string `json:"userId"`
	} `json:"details"`
}

func syncUser(ctx context.Context, kc *keycloak.Keycloak, bot *chatbot.Bot, id string) error {
	user, err := kc.GetUser(ctx, id)
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}

	if user.DiscordUserID > 0 {
		err = bot.SyncUser(ctx, &chatbot.UserStatus{
			ID:           user.DiscordUserID,
			ActiveMember: user.ActiveMember,
		})
		if err != nil {
			return fmt.Errorf("syncing discord user: %w", err)
		}
	}

	return nil
}
