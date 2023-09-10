package conf

type Env struct {
	KeycloakURL      string `split_words:"true" required:"true"`
	KeycloakUser     string `split_words:"true" required:"true"`
	KeycloakPassword string `split_words:"true" required:"true"`
	KeycloakRealm    string `default:"master" split_words:"true"`
}
