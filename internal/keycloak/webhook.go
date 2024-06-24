package keycloak

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type webhookMsg struct {
	ResourceType string `json:"resourceType"` // e.g. == "USER"
	Details      struct {
		UserID string `json:"userId"`
	} `json:"details"`
}

func NewWebhookHandler(fn func(userID string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msg := &webhookMsg{}
		err := json.NewDecoder(r.Body).Decode(msg)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		if msg.ResourceType != "USER" || msg.Details.UserID == "" {
			return
		}
		if !fn(msg.Details.UserID) {
			w.WriteHeader(500)
			return
		}
	})
}

func (k *Keycloak[T]) EnsureWebhook(ctx context.Context, callbackURL string) error {
	hooks, err := k.listWebhooks(ctx)
	if err != nil {
		return fmt.Errorf("listing: %w", err)
	}

	for _, hook := range hooks {
		if hook.URL == callbackURL {
			return nil // already exists
		}
	}

	return k.createWebhook(ctx, &Webhook{
		Enabled:    true,
		URL:        callbackURL,
		EventTypes: []string{"admin.*"},
	})
}

func (k *Keycloak[T]) listWebhooks(ctx context.Context) ([]*Webhook, error) {
	token, err := k.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	webhooks := []*Webhook{}
	_, err = k.Client.GetRequestWithBearerAuth(ctx, token.AccessToken).
		SetResult(&webhooks).
		Get(fmt.Sprintf("%s/realms/%s/webhooks", k.env.KeycloakURL, k.env.KeycloakRealm))
	if err != nil {
		return nil, err
	}

	return webhooks, nil
}

func (k *Keycloak[T]) createWebhook(ctx context.Context, webhook *Webhook) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	_, err = k.Client.GetRequestWithBearerAuth(ctx, token.AccessToken).
		SetBody(webhook).
		Post(fmt.Sprintf("%s/realms/%s/webhooks", k.env.KeycloakURL, k.env.KeycloakRealm))
	if err != nil {
		return err
	}

	return nil
}

type Webhook struct {
	ID         string   `json:"id"`
	Enabled    bool     `json:"enabled"`
	URL        string   `json:"url"`
	EventTypes []string `json:"eventTypes"`
}
