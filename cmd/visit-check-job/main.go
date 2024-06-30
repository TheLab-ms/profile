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

	if reporting.DefaultSink.Enabled() {
		users, err := kc.ListUsers(ctx)
		if err != nil {
			return fmt.Errorf("listing users: %w", err)
		}

		err = updateTimestamps(ctx, kc, users)
		if err != nil {
			return fmt.Errorf("updating timestamps: %w", err)
		}
	}

	users, err := kc.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	err = deactivateAbsentMembers(ctx, kc, users)
	if err != nil {
		return fmt.Errorf("deactivating absent members: %w", err)
	}

	err = deleteUnconfirmedAccounts(ctx, kc, users)
	if err != nil {
		return fmt.Errorf("deleting unconfirmed accounts: %w", err)
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

var absentThres = time.Hour * 24 * 365

func deactivateAbsentMembers(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], users []*keycloak.ExtendedUser[*datamodel.User]) error {
	limiter := rate.NewLimiter(rate.Every(time.Second), 1)
	for _, extended := range users {
		user := extended.User
		if !extended.ActiveMember || user.BuildingAccessApprover == "" || user.NonBillable {
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
		log.Printf("revoking build access approval for user %s %s (%s) because their last visit was %2.f days ago", user.First, user.Last, user.Email, sinceLastVisit.Hours()/24)
		user.BuildingAccessApprover = ""
		if err := kc.WriteUser(ctx, user); err != nil {
			log.Printf("error while deactivating user %s: %s", extended.User.UUID, err)
			continue
		}
		reporting.DefaultSink.Eventf(extended.User.Email, "RevokedBuildingAccessApproval", "removing building access approval because member hasn't visited in %2.f days", sinceLastVisit.Hours()/24)
	}
	return nil
}

func deleteUnconfirmedAccounts(ctx context.Context, kc *keycloak.Keycloak[*datamodel.User], users []*keycloak.ExtendedUser[*datamodel.User]) error {
	limiter := rate.NewLimiter(rate.Every(time.Second), 1)
	for _, extended := range users {
		if userIsConfirmed(extended) {
			continue
		}

		limiter.Wait(ctx)
		log.Printf("deleting user %s because they signed up %s ago and have not confirmed their email (status=%s, fobID=%d)", extended.User.Email, time.Since(extended.User.SignupTime).Round(time.Hour), extended.User.PaymentStatus(), extended.User.FobID)
		err := kc.DeleteUser(ctx, extended.User.UUID)
		if err != nil {
			log.Printf("error while deleting user %s: %s", extended.User.UUID, err)
			continue
		}
		reporting.DefaultSink.Eventf(extended.User.Email, "AccountCleanedUp", "account was deleted because its email address was not confirmed in the configured period")
	}
	return nil
}

func userIsConfirmed(user *keycloak.ExtendedUser[*datamodel.User]) bool {
	active := user.ActiveMember || user.User.EmailVerified || user.User.NonBillable
	tooNew := time.Since(user.User.SignupTime) < 48*time.Hour
	return active || tooNew
}
