package events

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
)

type Event struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Start       time.Time   `json:"scheduled_start_time"`
	End         time.Time   `json:"scheduled_end_time"`
	Recurrence  *Recurrence `json:"recurrence_rule"`
	Metadata    struct {
		Location string `json:"location"`
	} `json:"entity_metadata"`
}

type Recurrence struct {
	Start     time.Time  `json:"start"`
	End       *time.Time `json:"end"`
	Freq      int        `json:"frequency"`
	Interval  int        `json:"interval"`
	ByWeekday []int      `json:"by_weekday"`
	ByMonth   []int      `json:"by_month"`
}

type EventCache struct {
	mut     sync.Mutex
	state   []*Event
	refresh chan struct{}

	Env *conf.Env
}

func (e *EventCache) Refresh() { e.refresh <- struct{}{} }

func (e *EventCache) GetEvents() []*Event {
	e.mut.Lock()
	defer e.mut.Unlock()
	return e.state
}

func (e *EventCache) Start() {
	e.refresh = make(chan struct{}, 1)
	if e.Env.DiscordBotToken == "" || e.Env.DiscordGuildID == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		list := e.listEvents()

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

		// Wait until the timer or an explicit refresh
		select {
		case <-ticker.C:
		case <-e.refresh:
		}
	}()
}

func (e *EventCache) listEvents() []*Event {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://discord.com/api/v10/guilds/%s/scheduled-events", e.Env.DiscordGuildID), nil)
	if err != nil {
		log.Printf("error creating request to list discord events: %s", err)
		return nil
	}
	req.Header.Add("Authorization", "Bot "+e.Env.DiscordBotToken)

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

	events := []*Event{}
	err = json.NewDecoder(resp.Body).Decode(&events)
	if err != nil {
		log.Printf("got invalid json back from discord: %s", err)
		return nil
	}

	return events
}
