package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
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

	var err error
	reporting.DefaultSink, err = reporting.NewSink(env)
	if err != nil {
		log.Fatal(err)
	}

	kc := keycloak.New(env, nil)
	ctx := context.Background()

	users, err := kc.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	limiter := rate.NewLimiter(rate.Every(time.Millisecond*100), 1)
	for _, user := range users {
		if !user.ActiveMember {
			continue
		}
		limiter.Wait(ctx)

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

		err = kc.UpdateLastSwipeTime(ctx, user.User, latest)
		if err != nil {
			return fmt.Errorf("writing latest swipe to user: %w", err)
		}
		log.Printf("updated last visit time for user %q (%s->%s)", user.User.Email, user.LastSwipeTime, latest)
	}

	log.Printf("done!")
	return nil
}
