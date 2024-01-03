package eventing

import (
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx"

	"github.com/TheLab-ms/profile/internal/conf"
)

var DefaultSink *Sink

const migration = `
CREATE TABLE IF NOT EXISTS profile_events (
	id serial primary key,
	time timestamp not null,
	email text not null,
	reason text not null,
	message text not null
);

CREATE INDEX IF NOT EXISTS idx_profile_events_time ON profile_events (time);
`

type Sink struct {
	buffer chan *event
}

func NewSink(env *conf.Env) (*Sink, error) {
	s := &Sink{}
	if env.EventPsqlAddr == "" {
		return s, nil
	}

	db, err := pgx.Connect(pgx.ConnConfig{
		Host:     env.EventPsqlAddr,
		User:     env.EventPsqlUsername,
		Password: env.EventPsqlPassword,
	})
	if err != nil {
		return nil, fmt.Errorf("constructing db client: %w", err)
	}

	_, err = db.Exec(migration)
	if err != nil {
		return nil, fmt.Errorf("db migration: %w", err)
	}

	// Flush messages out to postgres
	s.buffer = make(chan *event, env.EventBufferLength)
	go func() {
		defer db.Close()

		for event := range s.buffer {
			_, err := db.Exec("INSERT INTO profile_events (time, email, reason, message) VALUES ($1, $2, $3, $4)", event.Timestamp, event.Email, event.Reason, event.Message)
			if err != nil {
				log.Printf("error while flushing event to postgres: %s", err) // it would be a good idea to retry here
			}

			// don't send messages too often
			// batching would be nice, this is easier to implement
			time.Sleep(time.Second)
		}
	}()

	return s, nil
}

func (s *Sink) Publish(email, reason, templ string, args ...any) {
	if s.buffer == nil {
		return
	}
	s.buffer <- &event{
		Timestamp: time.Now(),
		Email:     email,
		Reason:    reason,
		Message:   fmt.Sprintf(templ, args...),
	}
}

type event struct {
	Timestamp time.Time
	Email     string
	Reason    string
	Message   string
}
