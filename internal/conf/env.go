package conf

type Env struct {
	KeycloakURL            string `split_words:"true" required:"true"`
	KeycloakRealm          string `default:"master" split_words:"true"`
	KeycloakMembersGroupID string `split_words:"true" required:"true"`

	MaxUnverifiedAccounts int `split_words:"true" default:"50"`

	SelfURL          string `split_words:"true" required:"true"`
	StripeKey        string `split_words:"true"`
	StripeWebhookKey string `split_words:"true"`

	PaypalClientID     string `split_words:"true"`
	PaypalClientSecret string `split_words:"true"`

	DocusealURL   string `split_words:"true"`
	DocusealToken string `split_words:"true"`

	DiscordGuildID  string `split_words:"true"`
	DiscordBotToken string `split_words:"true"`

	// TODO: These should be prefixed "Reporting" instead of "Event"
	EventPsqlAddr     string `split_words:"true"`
	EventPsqlUsername string `split_words:"true"`
	EventPsqlPassword string `split_words:"true"`
	EventBufferLength int    `split_words:"true" default:"50"`

	// These should be loaded from the env if not set
	KeycloakClientID     string `split_words:"true"`
	KeycloakClientSecret string `split_words:"true"`
}
