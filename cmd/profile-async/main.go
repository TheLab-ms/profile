package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

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

	kc := keycloak.New(env)

	if env.KeycloakRegisterWebhook {
		err = kc.EnsureWebhook(context.TODO(), fmt.Sprintf("%s/webhooks/keycloak", env.SelfURL))
		if err != nil {
			log.Fatal(err)
		}
	}

	svr := &Server{Keycloak: kc}
	log.Fatal(http.ListenAndServe(":8080", svr.NewHandler()))
}

type Server struct {
	Keycloak *keycloak.Keycloak
}

func (s *Server) NewHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/webhooks/keycloak", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.Printf("(TODO don't log entire request body) got keycloak webhook: %s", string(body))
	})

	return mux
}
