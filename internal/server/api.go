package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/TheLab-ms/profile/internal/payment"
)

func (s *Server) newListEventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events, err := s.EventsCache.GetEvents(time.Now().Add(time.Hour * 24 * 60))
		if err != nil {
			renderSystemError(w, "getting cached events: %s", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		json.NewEncoder(w).Encode(events)
	}
}

func (s *Server) newPricingHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items := s.PriceCache.GetPrices()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		json.NewEncoder(w).Encode(newPrices(items))
	}
}

type prices struct {
	Yearly  price `json:"yearly"`
	Monthly price `json:"monthly"`
}

func newPrices(items []*payment.Price) *prices {
	// Pick the first yearly and monthly price that we find in the cache
	// based on no particular order (since we expect to only have one of each)
	var yearly, monthly *payment.Price
	for _, price := range items {
		price := price // GO WHY ARE YOU THIS WAY

		if price.Annual && yearly == nil {
			yearly = price
		}
		if !price.Annual && monthly == nil {
			monthly = price
		}
		if monthly != nil && yearly != nil {
			break
		}
	}

	// Convert to our API response types
	// When selecting discounts, pick the smallest i.e. most expensive.
	prices := &prices{}
	if yearly != nil {
		prices.Yearly.Price = yearly.Price

		var discount float64
		for _, c := range yearly.CouponAmountsOff {
			fd := float64(c) / 100
			if fd < discount || discount == 0 {
				discount = float64(c) / 100
			}
		}
		prices.Yearly.Discounted = prices.Yearly.Price - discount
	}

	if monthly != nil {
		prices.Monthly.Price = monthly.Price

		var discount float64
		for _, c := range monthly.CouponAmountsOff {
			fd := float64(c) / 100
			if fd < discount || discount == 0 {
				discount = float64(c) / 100
			}
		}
		prices.Monthly.Discounted = prices.Monthly.Price - discount
	}

	return prices
}

type price struct {
	Price      float64 `json:"price"`
	Discounted float64 `json:"discount"`
}
