package chatbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/TheLab-ms/profile/internal/conf"
)

type Discord struct {
	env *conf.Env
}

func NewDiscord(env *conf.Env) *Discord {
	if env.DiscordWebhookURL == "" {
		return nil
	}
	return &Discord{env: env}
}

func (d *Discord) NotifyNewMember(ctx context.Context, email string) {
	err := d.notifyNewMember(ctx, email)
	if err != nil {
		log.Printf("error while notifying Discord of new member registration: %s", err)
	}
}

func (d *Discord) notifyNewMember(ctx context.Context, email string) error {
	msg := map[string]any{
		"username": "Profile App",
		"content":  "A new account was created",
		"embeds": []any{
			map[string]any{
				"title": "User",
				"fields": []any{
					map[string]any{
						"name":  "Email",
						"value": email,
					},
					map[string]any{
						"name":  "Building Access",
						"value": fmt.Sprintf("[Enable](%s/admin/enable-building-access?email=%s)", d.env.SelfURL, email),
					},
				},
			},
		},
	}
	return d.sendMessage(ctx, &msg)
}

func (d *Discord) sendMessage(ctx context.Context, msg any) error {
	js, _ := json.Marshal(msg)
	buf := bytes.NewBuffer(js)
	req, err := http.NewRequestWithContext(ctx, "POST", d.env.DiscordWebhookURL, buf)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected response status: %d with body: %s", resp.StatusCode, body)
	}
	return nil
}
