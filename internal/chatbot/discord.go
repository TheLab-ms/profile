package chatbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/TheLab-ms/profile/internal/conf"
)

type DiscountStore interface {
	GetDiscountTypes() []string
}

type Discord struct {
	env       *conf.Env
	discounts DiscountStore
}

func NewDiscord(env *conf.Env, d DiscountStore) *Discord {
	if env.DiscordWebhookURL == "" {
		return nil
	}
	return &Discord{env: env, discounts: d}
}

func (d *Discord) NotifyNewMember(ctx context.Context, email string) {
	err := d.notifyNewMember(ctx, email)
	if err != nil {
		log.Printf("error while notifying Discord of new member registration: %s", err)
	}
}

func (d *Discord) notifyNewMember(ctx context.Context, email string) error {
	discountLinks := []string{}
	for _, kind := range d.discounts.GetDiscountTypes() {
		link := fmt.Sprintf("[Apply %s discount](%s/admin/apply-discount?email=%s&type=%s)", kind, d.env.SelfURL, email, kind)
		discountLinks = append(discountLinks, link)
	}

	msg := map[string]any{
		"username": "Profile App",
		"content":  "A new account was created",
		"embeds": []any{
			map[string]any{
				"fields": []any{
					map[string]any{
						"name":  "Email",
						"value": email,
					},
					map[string]any{
						"name":  "Building Access",
						"value": fmt.Sprintf("[Assign Fob](%s/admin/assign-fob?email=%s)", d.env.SelfURL, email),
					},
					map[string]any{
						"name":  "Discount",
						"value": strings.Join(discountLinks, " | "),
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
