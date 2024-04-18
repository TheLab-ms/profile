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

// TODO:
// - Profile shows only whether or not you have a keyfob, and maybe when it was issued or the last time it was scanned
// - Also message saying to ping folks to set up a new fob for you if you want
// - For access granters, click a message in the discord message to start process
//   - Tap your badge twice, then tap a new/unassigned badge
//   - The browser window will poll and tell you when the badge has been linked

// So we need:
// - A new link in the message
// - ...to a page viewed by the granter
// - JS to poll an admin endpoint that checks for the fob swipes

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

	// Price cache polls Stripe to load the configured prices, and is refreshed when they change (via webhook)
	priceCache := &payment.PriceCache{}
	priceCache.Start()

	discord := chatbot.NewDiscord(env, priceCache)
	kc := keycloak.New(env, discord)
	go kc.RunReportingLoop()

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
