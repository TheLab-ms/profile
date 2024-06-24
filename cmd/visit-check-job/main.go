package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/reporting"
	"golang.org/x/time/rate"
)

func main() {
	if err := run(); err != nil {
		log.Printf("terminal error: %s", err)
		os.Exit(1)
	}
}

func run() error {
	env := &conf.Env{}
	env.MustLoad()

	kc := keycloak.New[*datamodel.User](env)
	ctx := context.Background()

	var err error
	reporting.DefaultSink, err = reporting.NewSink(env, kc)
	if err != nil {
		log.Fatal(err)
	}
	kc.Sink = reporting.DefaultSink

	users, err := kc.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	err = updateTimestamps(ctx, kc, users)
	if err != nil {
		return fmt.Errorf("updating timestamps: %w", err)
	}

	users, err = kc.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	err = deactivateAbsentMembers(ctx, kc, users)
	if err != nil {
		return fmt.Errorf("deactivating absent members: %w", err)
	}

	log.Printf("done!")
	return nil
}

func updateTimestamps(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], users []*keycloak.ExtendedUser[*datamodel.User]) error {
	limiter := rate.NewLimiter(rate.Every(time.Millisecond*100), 1)
	for _, extended := range users {
		if !extended.ActiveMember {
			continue
		}
		user := extended.User

		name := fmt.Sprintf("%s %s", user.First, user.Last)
		latest, ok, err := reporting.DefaultSink.GetLatestSwipe(ctx, name, user.LastSwipeTime)
		if err != nil {
			return fmt.Errorf("getting latest swipe for user: %w", err)
		}
		if !ok {
			continue
		}

		if math.Abs(user.LastSwipeTime.Sub(latest).Seconds()) < 5 {
			continue // skip timestamps that are close
		}

		limiter.Wait(ctx)
		user.LastSwipeTime = latest
		err = kc.WriteUser(ctx, user)
		if err != nil {
			return fmt.Errorf("writing latest swipe to user: %w", err)
		}
		log.Printf("updated last visit time for user %q (%s->%s)", user.Email, user.LastSwipeTime, latest)
	}
	return nil
}

var saneStartTime = time.Now().Add(-(time.Hour * 24 * 365 * 10))

var absentThres = time.Hour * 24 * 100

func deactivateAbsentMembers(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], users []*keycloak.ExtendedUser[*datamodel.User]) error {
	limiter := rate.NewLimiter(rate.Every(time.Millisecond*100), 1)
	for _, extended := range users {
		user := extended.User
		if !extended.ActiveMember || user.NonBillable {
			continue
		}

		// If they last badged in more than 10yr ago, something is wrong
		if user.LastSwipeTime.Before(saneStartTime) {
			continue
		}

		// Ignore active members
		sinceLastVisit := time.Since(user.LastSwipeTime)
		if sinceLastVisit < absentThres {
			continue
		}

		limiter.Wait(ctx)
		log.Printf("[noop] Would have deactivated user %q because their last visit was %2.f days ago", user.Email, sinceLastVisit.Hours()/24)
	}
	return nil
}
