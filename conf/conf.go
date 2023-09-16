package conf

type Env struct {
	KeycloakURL            string `split_words:"true" required:"true"`
	KeycloakUser           string `split_words:"true" required:"true"`
	KeycloakPassword       string `split_words:"true" required:"true"`
	KeycloakRealm          string `default:"master" split_words:"true"`
	KeycloakClientID       string `split_words:"true" required:"true"`
	KeycloakMembersGroupID string `split_words:"true" required:"true"`

	SelfURL          string `split_words:"true" required:"true"`
	StripeKey        string `split_words:"true"`
	StripeWebhookKey string `split_words:"true"`

	MoodleURL     string `split_words:"true" required:"true"`
	MoodleWSToken string `split_words:"true" required:"true"`
	MoodleSecret  string `split_words:"true" required:"true"`
}
