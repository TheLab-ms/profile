package main

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stripe/stripe-go/v75"

	"github.com/TheLab-ms/profile/internal/chatbot"
	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/events"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/payment"
	"github.com/TheLab-ms/profile/internal/reporting"
	"github.com/TheLab-ms/profile/internal/server"
)

//go:embed assets/*
var assets embed.FS

//go:embed templates/*.html
var rawTemplates embed.FS

var templates *template.Template

func init() {
	// Parse the embedded templates once during initialization
	var err error
	templates, err = template.ParseFS(rawTemplates, "templates/*")
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	// Load the app's configuration from env vars bound to the config struct through magic
	env := &conf.Env{}
	env.MustLoad()

	// Stripe library (sadly) stores its creds in a global var
	stripe.Key = env.StripeKey

	// We use the Age CLI to encrypt/decrypt secrets
	// It requires the private key to be on disk, so we write it out from the config here.
	if env.AgePrivateKey != "" {
		if err := os.WriteFile("key.txt", []byte(env.AgePrivateKey), 0600); err != nil {
			log.Fatal(err)
		}
	}

	// Reporting allows meaningful actions taken by users to be stored somewhere for reference
	var err error
	reporting.DefaultSink, err = reporting.NewSink(env)
	if err != nil {
		log.Fatal(err)
	}

	discord := chatbot.NewDiscord()
	kc := keycloak.New(env, discord)
	go kc.RunReportingLoop()

	// Price cache polls Stripe to load the configured prices, and is refreshed when they change (via webhook)
	priceCache := &payment.PriceCache{}
	priceCache.Start()

	// Events cache polls a the Discord scheduled events API to feed the calendar API.
	eventsCache := events.NewCache(env)
	eventsCache.Start(context.Background())

	// Serve prometheus metrics on a separate port
	go func() {
		log.Fatal(http.ListenAndServe(":8081", promhttp.Handler()))
	}()

	// Run the main http server
	svr := &server.Server{
		Env:         env,
		Keycloak:    kc,
		PriceCache:  priceCache,
		EventsCache: eventsCache,
		Assets:      assets,
		Templates:   templates,
	}
	log.Fatal(http.ListenAndServe(":8080", svr.NewHandler()))
}
