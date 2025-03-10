package conf

import (
	"log"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// TODO: Use interface + getters

type Env struct {
	// Keycloak
	KeycloakURL             string `split_words:"true" required:"true"`
	KeycloakRealm           string `default:"master" split_words:"true"`
	KeycloakMembersGroupID  string `split_words:"true" required:"true"`
	KeycloakRegisterWebhook bool   `split_words:"true"`

	// These should be loaded from the env if not set
	KeycloakClientID     string `split_words:"true"`
	KeycloakClientSecret string `split_words:"true"`

	// App behavior related
	MaxUnverifiedAccounts int    `split_words:"true" default:"50"`
	SelfURL               string `split_words:"true" required:"true"`
	WebhookURL            string `split_words:"true"`

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
	DiscordAppID        string        `split_words:"true"`
	DiscordGuildID      string        `split_words:"true"`
	DiscordBotToken     string        `split_words:"true"`
	DiscordInterval     time.Duration `split_words:"true" default:"60s"`
	DiscordMemberRoleID string        `split_words:"true"`

	// Age (secrets encrpytion)
	AgePublicKey  string `split_words:"true"`
	AgePrivateKey string `split_words:"true"`

	// Reporting
	// TODO: These should be prefixed "Reporting" instead of "Event"
	EventPsqlAddr     string `split_words:"true"`
	EventPsqlUsername string `split_words:"true"`
	EventPsqlPassword string `split_words:"true"`
	EventBufferLength int    `split_words:"true" default:"50"`

	// Conway
	ConwayURL   string `split_words:"true"`
	ConwayToken string `split_words:"true"`
}

func (e *Env) MustLoad() {
	err := envconfig.Process("", e)
	if err != nil {
		log.Fatal(err)
	}
}
