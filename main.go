package main

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stripe/stripe-go/v75"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/events"
	"github.com/TheLab-ms/profile/internal/files"
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

var (
	inFlightGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "in_flight_requests",
		Help: "A gauge of requests currently being served",
	})

	requestCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "requests_total",
			Help: "HTTP request counter by code and method",
		},
		[]string{"code", "method"},
	)
)

func init() {
	prometheus.MustRegister(inFlightGauge, requestCounter)

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

	kc := keycloak.New(env)
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

	// Run the file server on a different port
	//
	// This is designed to be separate from the main server for a few reasons:
	// it's the only part of the app that isn't stateless, and it makes sense for
	// it to live on the LAN - not out on the internet like this app.
	//
	// Files are limited size and only persist for 24hr.
	//
	// TODO: Make this server more modular and flexible
	if env.FileUploadDir != "" {
		files.StartCleanupLoop(context.Background(), env.FileUploadDir, time.Hour*24)

		go func() {
			handler := files.NewFileServerHandler([]byte(env.FileTokenSigningKey), env.FileUploadDir)
			log.Fatal(http.ListenAndServe(":8888",
				promhttp.InstrumentHandlerInFlight(inFlightGauge,
					promhttp.InstrumentHandlerCounter(requestCounter,
						handler))))
		}()
	}

	// Run the main http server
	svr := &server.Server{
		Env:         env,
		Keycloak:    kc,
		PriceCache:  priceCache,
		EventsCache: eventsCache,
		Assets:      assets,
		Templates:   templates,
	}
	log.Fatal(http.ListenAndServe(":8080",
		promhttp.InstrumentHandlerInFlight(inFlightGauge,
			promhttp.InstrumentHandlerCounter(requestCounter,
				svr.NewHandler()))))
}
