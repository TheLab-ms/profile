package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/TheLab-ms/profile/internal/datamodel"
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
		json.NewEncoder(w).Encode(datamodel.NewPrices(items))
	}
}
