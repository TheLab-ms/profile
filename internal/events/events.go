package events

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/timeutil"
	"github.com/teambition/rrule-go"
)

// EventCache polls Discord events, caches them in-memory, and materializes recurring events.
type EventCache struct {
	timeutil.Loop
	mut   sync.Mutex
	state []*event

	env     *conf.Env
	baseURL string
}

func NewCache(env *conf.Env) *EventCache {
	ec := &EventCache{env: env, baseURL: "https://discord.com"}
	ec.Loop.Handler = ec.fillCache
	ec.Loop.Interval = env.DiscordInterval
	return ec
}

func (e *EventCache) GetEvents(until time.Time) ([]*Event, error) {
	now := time.Now()

	e.mut.Lock()
	events := e.state
	e.mut.Unlock()

	// Expand the recurrence of every event
	var expanded []*Event
	for _, event := range events {
		// Support a magic location string to designate members only events
		membersOnly := strings.Contains(strings.ToLower(event.Name), "(member event)")

		if event.Recurrence == nil {
			expanded = append(expanded, &Event{
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
			return nil, fmt.Errorf("expanding recurring events: %w", err)
		}

		// Our expansion ends either when the "until" is reached or when the event's recurrence ends
		var end time.Time
		if event.Recurrence.End != nil {
			end = *event.Recurrence.End
		} else {
			end = until
		}

		// Calculate the end time by adding the duration of the event to the start time
		times := rule.Between(now, end, true)
		duration := event.End.Sub(event.Start)
		for _, start := range times {
			expanded = append(expanded, &Event{
				Name:        event.Name,
				Description: event.Description,
				Start:       start.UTC().Unix(),
				End:         start.Add(duration).Unix(),
				MembersOnly: membersOnly,
			})
		}
	}

	sort.Slice(expanded, func(i, j int) bool { return expanded[i].Start < expanded[j].Start })
	return expanded, nil
}

func (e *EventCache) fillCache(ctx context.Context) {
	// Don't run if not configured
	if e.env.DiscordBotToken == "" || e.env.DiscordGuildID == "" {
		return
	}

	list := e.listEvents(ctx)

	e.mut.Lock()
	if list == nil && e.state == nil {
		log.Fatalf("failed to warm Discord events cache, and no previous cache was set - exiting")
		e.mut.Unlock()
		return
	}
	if list == nil {
		e.mut.Unlock()
		return
	}
	e.state = list
	log.Printf("updated cache of %d events", len(list))
	e.mut.Unlock()
}

func (e *EventCache) listEvents(ctx context.Context) []*event {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/v10/guilds/%s/scheduled-events", e.baseURL, e.env.DiscordGuildID), nil)
	if err != nil {
		log.Printf("error creating request to list discord events: %s", err)
		return nil
	}
	req.Header.Add("Authorization", "Bot "+e.env.DiscordBotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("error sending http request to list discord events: %s", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("unexpected response status from discord while listing events: %q - body: %s", resp.StatusCode, body)
		return nil
	}

	events := []*event{}
	err = json.NewDecoder(resp.Body).Decode(&events)
	if err != nil {
		log.Printf("got invalid json back from discord: %s", err)
		return nil
	}

	return events
}

// event is a partial representation of the Discord scheduled events API schema.
type event struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Start       time.Time   `json:"scheduled_start_time"`
	End         time.Time   `json:"scheduled_end_time"`
	Recurrence  *recurrence `json:"recurrence_rule"`
	Metadata    struct {
		Location string `json:"location"`
	} `json:"entity_metadata"`
}

type recurrence struct {
	Start     time.Time  `json:"start"`
	End       *time.Time `json:"end"`
	Freq      int        `json:"frequency"`
	Interval  int        `json:"interval"`
	ByWeekday []int      `json:"by_weekday"`
	ByMonth   []int      `json:"by_month"`
}

type Event struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	MembersOnly bool   `json:"membersOnly"`
}
