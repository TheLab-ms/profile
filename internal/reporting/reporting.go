package reporting

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"

	"github.com/TheLab-ms/profile/internal/conf"
)

var DefaultSink *ReportingSink

const migration = `
CREATE TABLE IF NOT EXISTS profile_events (
	id serial primary key,
	time timestamp not null,
	email text not null,
	reason text not null,
	message text not null
);

CREATE INDEX IF NOT EXISTS idx_profile_events_time ON profile_events (time);

CREATE TABLE IF NOT EXISTS profile_metrics (
	id serial primary key,
	time timestamp not null,
	active_members int not null,
	inactive_members int not null
);

CREATE INDEX IF NOT EXISTS idx_profile_metrics_time ON profile_metrics (time);
ALTER TABLE profile_metrics ADD COLUMN IF NOT EXISTS unverified_accounts int;
`

// ReportingSink buffers and periodically flushes meaningful user actions to postgres.
type ReportingSink struct {
	db     *pgxpool.Pool
	buffer chan *event
}

func NewSink(env *conf.Env) (*ReportingSink, error) {
	s := &ReportingSink{}
	if env.EventPsqlAddr == "" {
		return s, nil
	}

	db, err := pgxpool.Connect(context.Background(), fmt.Sprintf("user=%s password=%s host=%s port=5432 dbname=postgres", env.EventPsqlUsername, env.EventPsqlPassword, env.EventPsqlAddr))
	if err != nil {
		return nil, fmt.Errorf("constructing db client: %w", err)
	}
	s.db = db

	_, err = db.Exec(context.Background(), migration)
	if err != nil {
		return nil, fmt.Errorf("db migration: %w", err)
	}

	// Flush messages out to postgres
	s.buffer = make(chan *event, env.EventBufferLength)
	go func() {
		defer db.Close()

		for event := range s.buffer {
			_, err := db.Exec(context.Background(), "INSERT INTO profile_events (time, email, reason, message) VALUES ($1, $2, $3, $4)", event.Timestamp, event.Email, event.Reason, event.Message)
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

func (s *ReportingSink) Publish(email, reason, templ string, args ...any) {
	if s == nil || s.buffer == nil {
		return
	}
	if email == "" {
		email = "<unknown>"
	}
	s.buffer <- &event{
		Timestamp: time.Now(),
		Email:     email,
		Reason:    reason,
		Message:   fmt.Sprintf(templ, args...),
	}
}

func (s *ReportingSink) LastMetricTime() (time.Time, error) {
	t := time.Time{}
	return t, s.db.QueryRow(context.Background(), "SELECT COALESCE(MAX(time), '0001-01-01'::timestamp) AS time FROM profile_metrics").Scan(&t)
}

func (s *ReportingSink) WriteMetrics(counters *Counters) error {
	_, err := s.db.Exec(context.Background(), "INSERT INTO profile_metrics (time, active_members, inactive_members, unverified_accounts) VALUES ($1, $2, $3, $4)", time.Now(), counters.ActiveMembers, counters.InactiveMembers, counters.UnverifiedAccounts)
	return err
}

func (s *ReportingSink) GetLatestSwipe(ctx context.Context, name string, last time.Time) (time.Time, bool, error) {
	swipe := time.Time{}
	err := s.db.QueryRow(ctx, "SELECT time FROM swipes WHERE name = $1 AND time > $2 ORDER BY time DESC LIMIT 1", name, last).Scan(&swipe)
	if err != nil && strings.Contains(err.Error(), "no rows in result set") {
		return swipe, false, nil // errors.Is didn't work with the psql library for some reason
	}
	return swipe, true, err
}

// This really doesn't belong on the reporting sink, but it queries the reporting DB so it's convenient to put it here.
func (s *ReportingSink) LastFobAssignment(ctx context.Context, granterFobID int) (int, bool, error) {
	var prevID int
	err := s.db.QueryRow(ctx, "SELECT MAX(id) FROM swipes WHERE cardID = $1 AND seenAt >= NOW() - INTERVAL '1 minute' GROUP BY time ORDER BY time DESC", granterFobID).Scan(&prevID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows in result set") {
			log.Printf("no swipe found for granter's fob %d", granterFobID)
			return 0, false, nil // errors.Is didn't work with the psql library for some reason
		}
		return 0, false, err
	}
	log.Printf("found previous swipe ID of %d", prevID)

	// Look up the next swipe
	var id int
	err = s.db.QueryRow(ctx, "SELECT cardID FROM swipes WHERE id = $1 AND seenAt >= NOW() - INTERVAL '1 minute'", prevID+1).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "no rows in result set") {
			log.Printf("no swipe found for new fob")
			return 0, false, nil // errors.Is didn't work with the psql library for some reason
		}
		return 0, false, err
	}

	return id, true, nil
}

func (s *ReportingSink) Enabled() bool { return s != nil && s.db != nil }

type Counters struct {
	ActiveMembers      int64
	InactiveMembers    int64
	UnverifiedAccounts int64
}

type event struct {
	Timestamp time.Time
	Email     string
	Reason    string
	Message   string
}
