package conf

import (
	"log"

	"github.com/kelseyhightower/envconfig"
)

type Env struct {
	// Keycloak
	KeycloakURL            string `split_words:"true" required:"true"`
	KeycloakRealm          string `default:"master" split_words:"true"`
	KeycloakMembersGroupID string `split_words:"true" required:"true"`

	// These should be loaded from the env if not set
	KeycloakClientID     string `split_words:"true"`
	KeycloakClientSecret string `split_words:"true"`

	// App behavior related
	MaxUnverifiedAccounts int    `split_words:"true" default:"50"`
	SelfURL               string `split_words:"true" required:"true"`

	// Stripe
	StripeKey        string `split_words:"true"`
	StripeWebhookKey string `split_words:"true"`

	// Paypal (for migration)
	PaypalClientID     string `split_words:"true"`
	PaypalClientSecret string `split_words:"true"`

	// Docuseal
	DocusealURL   string `split_words:"true"`
	DocusealToken string `split_words:"true"`

	// Discord
	DiscordGuildID  string `split_words:"true"`
	DiscordBotToken string `split_words:"true"`

	// Reporting
	// TODO: These should be prefixed "Reporting" instead of "Event"
	EventPsqlAddr     string `split_words:"true"`
	EventPsqlUsername string `split_words:"true"`
	EventPsqlPassword string `split_words:"true"`
	EventBufferLength int    `split_words:"true" default:"50"`
}

func (e *Env) MustLoad() {
	err := envconfig.Process("", e)
	if err != nil {
		log.Fatal(err)
	}
}
