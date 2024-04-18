package reporting

import (
	"context"
	"database/sql"
	"errors"
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
	_, err := s.db.Exec(context.Background(), "INSERT INTO profile_metrics (time, active_members, inactive_members) VALUES ($1, $2, $3)", time.Now(), counters.ActiveMembers, counters.InactiveMembers)
	return err
}

const fobQuery = `
WITH consecutive_swipes AS (
    SELECT
        s1.cardID AS card1,
        s1.time AS time1,
        s2.cardID AS card2,
        s2.time AS time2,
        s1.name
    FROM
        swipes s1
    JOIN
        swipes s2 ON s1.time = (s2.time - interval '15 seconds') AND s1.doorID = s2.doorID
)
SELECT
    MAX(time1) AS last_swipe_time,
    name,
    card2
FROM
    consecutive_swipes
WHERE
    card1 <> card2
    AND name = $1
GROUP BY
    name, card2
ORDER BY
    last_swipe_time DESC
LIMIT 1;
`

// This really doesn't belong on the reporting sink, but it queries the reporting DB so it's convenient to put it here.
func (s *ReportingSink) LastFobAssignment(ctx context.Context, granterUUID string) (int64, bool, error) {
	uuid := strings.ReplaceAll(granterUUID, "-", "") // the access controller can't store dashes

	var id int64
	err := s.db.QueryRow(ctx, fobQuery, uuid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (s *ReportingSink) Enabled() bool { return s != nil && s.db != nil }

type Counters struct {
	ActiveMembers   int64
	InactiveMembers int64
}

type event struct {
	Timestamp time.Time
	Email     string
	Reason    string
	Message   string
}
