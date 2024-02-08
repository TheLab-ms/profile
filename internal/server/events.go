package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

type eventPublic struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	MembersOnly bool   `json:"membersOnly"`
}

func (s *Server) newListEventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		events := s.EventsCache.GetEvents()

		// Expand the recurrence of every event
		var expanded []*eventPublic
		for _, event := range events {
			// Support a magic location string to designate members only events
			membersOnly := strings.Contains(strings.ToLower(event.Name), "(member event)")

			if event.Recurrence == nil {
				expanded = append(expanded, &eventPublic{
					Name:        event.Name,
					Description: event.Description,
					Start:       event.Start.UTC().Unix(),
					End:         event.End.UTC().Unix(),
					MembersOnly: membersOnly,
				})
				continue
			}

			// Expand out the recurring events into a slice of start times
			ropts := rrule.ROption{
				Freq:     rrule.Frequency(event.Recurrence.Freq),
				Interval: event.Recurrence.Interval,
				Dtstart:  event.Recurrence.Start.UTC(),
				Bymonth:  event.Recurrence.ByMonth,
			}
			for _, day := range event.Recurrence.ByWeekday {
				// annoying that the library doesn't expose days of the week as ints - they line up with discord's representation anyway
				switch day {
				case 0:
					ropts.Byweekday = append(ropts.Byweekday, rrule.MO)
				case 1:
					ropts.Byweekday = append(ropts.Byweekday, rrule.TU)
				case 2:
					ropts.Byweekday = append(ropts.Byweekday, rrule.WE)
				case 3:
					ropts.Byweekday = append(ropts.Byweekday, rrule.TH)
				case 4:
					ropts.Byweekday = append(ropts.Byweekday, rrule.FR)
				case 5:
					ropts.Byweekday = append(ropts.Byweekday, rrule.SA)
				case 6:
					ropts.Byweekday = append(ropts.Byweekday, rrule.SU)
				}
			}
			rule, err := rrule.NewRRule(ropts)
			if err != nil {
				renderSystemError(w, "error expanding recurrence: %s", err.Error())
				return
			}

			// Our expansion ends either 1mo out from now or when the recurrence ends
			var end time.Time
			if event.Recurrence.End != nil {
				end = *event.Recurrence.End
			} else {
				end = now.Add(time.Hour * 24 * 30)
			}

			// Calculate the end time by adding the duration of the event to the start time
			times := rule.Between(now, end, true)
			duration := event.End.Sub(event.Start)
			for _, start := range times {
				expanded = append(expanded, &eventPublic{
					Name:        event.Name,
					Description: event.Description,
					Start:       start.UTC().Unix(),
					End:         start.Add(duration).Unix(),
					MembersOnly: membersOnly,
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		json.NewEncoder(w).Encode(&expanded)
	}
}
