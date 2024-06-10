package attendance

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/reporting"
	"github.com/TheLab-ms/profile/internal/timeutil"
)

// TODO: Maybe make this a k8s job?

type Loop struct {
	timeutil.Loop
	keycloak  *keycloak.Keycloak
	reporting *reporting.ReportingSink
}

func NewLoop(kc *keycloak.Keycloak, r *reporting.ReportingSink) *Loop {
	l := &Loop{keycloak: kc, reporting: r}
	l.Loop = timeutil.Loop{
		Handler:  l.tick,
		Interval: time.Hour * 24,
	}
	return l
}

func (l *Loop) tick(ctx context.Context) {
	log.Printf("starting to scrape latest badge swipe times for each active user")

	users, err := l.keycloak.ListUsers(ctx)
	if err != nil {
		log.Printf("error listing users: %s", err)
		return
	}
	for _, user := range users {
		if !user.ActiveMember {
			continue
		}

		name := fmt.Sprintf("%s %s", user.First, user.Last)
		latest, ok, err := l.reporting.GetLatestSwipe(ctx, name, user.LastSwipeTime)
		if err != nil {
			log.Printf("error looking up latest swipe for user: %s", err)
			break
		}
		if !ok {
			continue
		}

		if math.Abs(user.LastSwipeTime.Sub(latest).Seconds()) < 5 {
			continue // skip timestamps that are close
		}

		err = l.keycloak.UpdateLastSwipeTime(ctx, user.User, latest)
		if err != nil {
			log.Printf("error writing latest swipe to user: %s", err)
			break
		}
	}
}
