package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stripe/stripe-go/v75"

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
	eventsCache := &events.EventCache{Env: env}
	eventsCache.Start()

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
	log.Fatal(http.ListenAndServe(":8080",
		promhttp.InstrumentHandlerInFlight(inFlightGauge,
			promhttp.InstrumentHandlerCounter(requestCounter,
				svr.NewHandler()))))
}
