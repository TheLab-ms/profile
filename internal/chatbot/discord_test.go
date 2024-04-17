package chatbot

import (
	"context"
	"os"
	"testing"

	"github.com/TheLab-ms/profile/internal/conf"
)

func TestIntegration(t *testing.T) {
	url := os.Getenv("TEST_WEBHOOK_URL")
	if url == "" {
		t.Skip()
	}

	d := NewDiscord(&conf.Env{
		DiscordWebhookURL: url,
		SelfURL:           "https://localhost:8080",
	})
	d.NotifyNewMember(context.Background(), "cto+test@thelab.ms")
}
