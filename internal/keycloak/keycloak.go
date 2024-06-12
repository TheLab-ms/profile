package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/stripe/stripe-go/v75"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/reporting"
)

var (
	ErrConflict      = errors.New("conflict")
	ErrLimitExceeded = errors.New("limit exceeded")
)

type Keycloak struct {
	Client *gocloak.GoCloak
	env    *conf.Env

	// use ensureToken to access these
	tokenLock      sync.Mutex
	token          *gocloak.JWT
	tokenFetchTime time.Time
}

func New(c *conf.Env) *Keycloak {
	return &Keycloak{Client: gocloak.NewClient(c.KeycloakURL), env: c}
}

// RegisterUser creates a user and initiates the password reset + email confirmation flow.
// Currently the two steps do not occur atomically - we assume the system will not crash between them.
func (k *Keycloak) RegisterUser(ctx context.Context, email string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	n, err := k.Client.GetUserCount(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{EmailVerified: gocloak.BoolP(false)})
	if err != nil {
		return fmt.Errorf("counting users with unverified email addresses: %w", err)
	}
	if n > k.env.MaxUnverifiedAccounts {
		return ErrLimitExceeded
	}

	userID, err := k.Client.CreateUser(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.User{
		Enabled:  gocloak.BoolP(true),
		Email:    &email,
		Username: &email,
		Attributes: &map[string][]string{
			"signupEpochTimeUTC": {strconv.Itoa(int(time.Now().UTC().Unix()))},
		},
	})
	if err != nil {
		if e, ok := err.(*gocloak.APIError); ok && e.Code == 409 {
			return ErrConflict
		}
		return fmt.Errorf("creating user: %w", err)
	}

	clientID, err := k.getClientID()
	if err != nil {
		return err
	}

	resp, err := k.Client.GetRequestWithBearerAuth(ctx, token.AccessToken).
		SetQueryParams(map[string]string{"lifespan": "43200", "redirect_uri": k.env.SelfURL + "/profile", "client_id": string(clientID)}).
		SetBody([]string{"UPDATE_PASSWORD", "VERIFY_EMAIL"}).
		Put(fmt.Sprintf("%s/admin/realms/%s/users/%s/execute-actions-email", k.env.KeycloakURL, k.env.KeycloakRealm, userID))
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	if resp.IsError() {
		return fmt.Errorf("unknown error from keycloak: %s", resp.Body())
	}

	return nil
}

func (k *Keycloak) BadgeIDInUse(ctx context.Context, id int) (bool, error) {
	token, err := k.GetToken(ctx)
	if err != nil {
		return false, fmt.Errorf("getting token: %w", err)
	}

	users, err := k.Client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
		Q:   gocloak.StringP(fmt.Sprintf("keyfobID:%d", id)),
		Max: gocloak.IntP(1),
	})
	if err != nil {
		return false, fmt.Errorf("counting users with unverified email addresses: %w", err)
	}
	return len(users) > 0, nil
}

func (k *Keycloak) GetUser(ctx context.Context, userID string) (*User, error) {
	token, err := k.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	kcuser, err := k.Client.GetUserByID(ctx, token.AccessToken, k.env.KeycloakRealm, userID)
	if err != nil {
		return nil, err
	}

	return newUser(kcuser)
}

// GetUserAtETag returns the user object at the given etag, or a possibly different version on timeout.
//
// This is useful when avoiding backtracking for cases in which a change has been written to Stripe but
// the corresponding webhook may not have been handled.
func (k *Keycloak) GetUserAtETag(ctx context.Context, userID string, etag int64) (user *User, err error) {
	for i := 0; i < 15; i++ {
		user, err = k.GetUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		if etag == 0 || user.StripeETag >= etag {
			return user, nil
		}

		time.Sleep(time.Millisecond * 100) // backoff + jitter would be nice here
	}
	log.Printf("timeout while waiting for Stripe webhook")
	return user, nil // timeout
}

func (k *Keycloak) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	token, err := k.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	kcusers, err := k.Client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
		Email: &email,
	})
	if err != nil {
		return nil, fmt.Errorf("getting current user: %w", err)
	}
	if len(kcusers) == 0 {
		return nil, errors.New("user not found")
	}

	return newUser(kcusers[0])
}

func (k *Keycloak) EnableUserBuildingAccess(ctx context.Context, user *User, approver string, fobID int) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	attr := safeGetAttrs(user.keycloakObject)
	attr["buildingAccessApprover"] = []string{approver}
	attr["keyfobID"] = []string{strconv.Itoa(fobID)}

	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject)
}

func (k *Keycloak) ApplyDiscount(ctx context.Context, user *User, discountType string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	attr := safeGetAttrs(user.keycloakObject)
	attr["discountType"] = []string{discountType}

	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject)
}

func (k *Keycloak) UpdateUserWaiverState(ctx context.Context, user *User) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	attr := safeGetAttrs(user.keycloakObject)
	attr["waiverState"] = []string{"Signed"}

	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject)
}

func (k *Keycloak) UpdateUserName(ctx context.Context, user *User, first, last string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	user.keycloakObject.FirstName = &first
	user.keycloakObject.LastName = &last
	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject)
}

func (k *Keycloak) UpdateLastSwipeTime(ctx context.Context, user *User, ts time.Time) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	attr := safeGetAttrs(user.keycloakObject)
	attr["lastSwipeTime"] = []string{strconv.Itoa(int(ts.UTC().Unix()))}
	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject)
}

func (k *Keycloak) UpdateDiscordLink(ctx context.Context, user *User, id string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	attr := safeGetAttrs(user.keycloakObject)
	attr["discordUserID"] = []string{id}
	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject)
}

func (k *Keycloak) UpdatePaypalMetadata(ctx context.Context, user *User, price float64, lastTX time.Time) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	buf, err := json.Marshal(&paypalMetadata{
		Price:         price,
		TimeRFC3339:   lastTX.Format(time.RFC3339),
		TransactionID: user.PaypalSubscriptionID,
	})
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}

	attr := safeGetAttrs(user.keycloakObject)
	attr["paypalMigrationMetadata"] = []string{string(buf)}
	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject)
}

func (k *Keycloak) Deactivate(ctx context.Context, user *User) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	err = k.Client.DeleteUserFromGroup(ctx, token.AccessToken, k.env.KeycloakRealm, *user.keycloakObject.ID, k.env.KeycloakMembersGroupID)
	if err != nil {
		return err
	}

	// TODO: Leaky abstraction
	reporting.DefaultSink.Publish(user.Email, "PayPalSubscriptionCanceled", "We observed the member's PayPal status in an inactive state")
	return nil
}

func (k *Keycloak) UpdateUserStripeInfo(ctx context.Context, user *User, customer *stripe.Customer, sub *stripe.Subscription) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcuser := user.keycloakObject
	attr := safeGetAttrs(kcuser)
	active := sub.Status == stripe.SubscriptionStatusActive || sub.Status == stripe.SubscriptionStatusTrialing

	// Don't de-activate accounts when we receive cancelation webhooks for a subscription that is not currently in use.
	// This shouldn't be possible for any accounts other than tests.
	if !active && !strings.EqualFold(user.StripeSubscriptionID, sub.ID) {
		log.Printf("dropping cancelation webhook for user %s because the subscription ID doesn't match the one in keycloak", *kcuser.Email)
		return nil
	}

	// Always clean up any old paypal metadata
	attr["paypalMigrationMetadata"] = []string{}

	if active {
		attr["stripeID"] = []string{customer.ID}
		attr["stripeSubscriptionID"] = []string{sub.ID}
		attr["stripeCancelationTime"] = []string{strconv.FormatInt(sub.CancelAt, 10)}

		if customer.ID != user.StripeCustomerID || sub.ID != user.StripeSubscriptionID {
			reporting.DefaultSink.Publish(user.Email, "StripeSubscriptionChanged", "A Stripe webhook caused the user's Stripe customer and/or subscription to change")
		} else if user.StripeCancelationTime == 0 && sub.CancelAt > 0 {
			reporting.DefaultSink.Publish(user.Email, "StripeSubscriptionCanceled", "The user canceled their subscription")
		}
	} else {
		// Revoke building access when payment is canceled
		attr["buildingAccessApprover"] = []string{""}
	}

	if sub.Metadata != nil {
		attr["stripeETag"] = []string{sub.Metadata["etag"]}
	}

	err = k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
	if err != nil {
		return fmt.Errorf("updating user: %w", err)
	}

	groups, err := k.Client.GetUserGroups(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, gocloak.GetGroupsParams{
		Search: gocloak.StringP("thelab-members"),
	})
	if err != nil {
		return fmt.Errorf("listing user groups: %w", err)
	}

	// Users should only be in the members group when their Stripe subscription is active
	inGroup := len(groups) > 0
	if !inGroup && active {
		reporting.DefaultSink.Publish(user.Email, "MembershipActivated", "A change in payment state caused the user's membership to be enabled")
		err = k.Client.AddUserToGroup(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, k.env.KeycloakMembersGroupID)
	}
	if inGroup && !active {
		reporting.DefaultSink.Publish(user.Email, "MembershipDeactivated", "A change in payment state caused the user's membership to be disabled")
		err = k.Client.DeleteUserFromGroup(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, k.env.KeycloakMembersGroupID)
	}
	if err != nil {
		return fmt.Errorf("updating user group membership: %w", err)
	}

	return nil
}

func (k *Keycloak) ListUsers(ctx context.Context) ([]*ExtendedUser, error) {
	token, err := k.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	var (
		max           = 50
		first         = 0
		activeMembers = map[string]struct{}{}
	)
	for {
		params, err := gocloak.GetQueryParams(gocloak.GetUsersParams{
			BriefRepresentation: gocloak.BoolP(true),
			Max:                 &max,
			First:               &first,
		})
		if err != nil {
			return nil, err
		}

		// Unfortunately the keycloak client doesn't support the group membership endpoint.
		// We reuse the client's transport here while specifying our own URL.
		var memberships []*gocloak.User
		_, err = k.Client.GetRequestWithBearerAuth(ctx, token.AccessToken).
			SetResult(&memberships).
			SetQueryParams(params).
			Get(fmt.Sprintf("%s/admin/realms/%s/groups/%s/members", k.env.KeycloakURL, k.env.KeycloakRealm, k.env.KeycloakMembersGroupID))
		if err != nil {
			return nil, err
		}
		if len(memberships) == 0 {
			break
		}
		first += len(memberships)

		for _, member := range memberships {
			activeMembers[gocloak.PString(member.ID)] = struct{}{}
		}
	}

	parsedUsers := []*ExtendedUser{}
	max = 50
	first = 0
	for {
		users, err := k.Client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{Max: &max, First: &first})
		if err != nil {
			return nil, fmt.Errorf("getting token: %w", err)
		}
		if len(users) == 0 {
			return parsedUsers, nil
		}
		first += len(users)
		for _, kcuser := range users {
			user, err := newUser(kcuser)
			if err != nil {
				log.Printf("error while parsing user %q in list: %s", gocloak.PString(kcuser.ID), err)
				continue
			}

			_, member := activeMembers[gocloak.PString(kcuser.ID)]
			fullUser := &ExtendedUser{User: user, ActiveMember: member}
			parsedUsers = append(parsedUsers, fullUser)
		}
	}
}

// For whatever reason the Keycloak client doesn't support token rotation
func (k *Keycloak) GetToken(ctx context.Context) (*gocloak.JWT, error) {
	k.tokenLock.Lock()
	defer k.tokenLock.Unlock()

	if k.token != nil && time.Since(k.tokenFetchTime) < (time.Duration(k.token.ExpiresIn)*time.Second)/2 {
		return k.token, nil
	}

	clientID, err := k.getClientID()
	if err != nil {
		return nil, err
	}
	clientSecret, err := k.getClientSecret()
	if err != nil {
		return nil, err
	}

	token, err := k.Client.LoginClient(ctx, string(clientID), string(clientSecret), k.env.KeycloakRealm)
	if err != nil {
		return nil, err
	}
	k.token = token
	k.tokenFetchTime = time.Now()

	log.Printf("fetched new auth token from keycloak - will expire in %d seconds", k.token.ExpiresIn)
	return k.token, nil
}

func (k *Keycloak) getClientID() (string, error) {
	if len(k.env.KeycloakClientID) > 0 {
		return k.env.KeycloakClientID, nil
	}
	clientID, err := os.ReadFile("/var/lib/keycloak/client-id")
	if err != nil {
		return "", fmt.Errorf("reading client id from disk: %w", err)
	}
	return string(clientID), nil
}

func (k *Keycloak) getClientSecret() (string, error) {
	if len(k.env.KeycloakClientSecret) > 0 {
		return k.env.KeycloakClientSecret, nil
	}
	clientSecret, err := os.ReadFile("/var/lib/keycloak/client-secret")
	if err != nil {
		return "", fmt.Errorf("reading client secret from disk: %w", err)
	}
	return string(clientSecret), nil
}

func (k *Keycloak) RunReportingLoop() {
	if !reporting.DefaultSink.Enabled() {
		return // no db to report to
	}

	const interval = time.Hour * 24
	const retryInterval = time.Second * 10
	go func() {
		timer := time.NewTimer(retryInterval)
		for range timer.C {
			lastTime, err := reporting.DefaultSink.LastMetricTime()
			if err != nil {
				log.Printf("error while getting the last metrics reporting time: %s", err)
				timer.Reset(retryInterval)
				continue
			}

			delta := interval - time.Since(lastTime)
			if delta > 0 {
				log.Printf("it isn't time to report metrics yet - setting time for %d seconds in the future", int(delta.Seconds()))
				timer.Reset(delta)
				continue
			}

			k.reportMetrics()
			timer.Reset(retryInterval) // don't wait the entire interval in case reportMetrics failed
		}
	}()
}

func (k *Keycloak) reportMetrics() {
	users, err := k.ListUsers(context.Background())
	if err != nil {
		log.Printf("error listing users to derive metrics: %s", err)
		return
	}

	counters := reporting.Counters{}
	for _, user := range users {
		if !user.EmailVerified {
			counters.UnverifiedAccounts++
		}
		if user.ActiveMember {
			counters.ActiveMembers++
		} else {
			counters.InactiveMembers++
		}
	}

	err = reporting.DefaultSink.WriteMetrics(&counters)
	if err != nil {
		log.Printf("unable to write metrics to the reporting store: %s", err)
		return
	}
}

func safeGetAttrs(kcuser *gocloak.User) map[string][]string {
	if kcuser.Attributes != nil {
		return *kcuser.Attributes
	}
	attr := map[string][]string{}
	kcuser.Attributes = &attr
	return attr

}

func safeGetAttr(kcuser *gocloak.User, key string) string {
	return firstElOrZeroVal(safeGetAttrs(kcuser)[key])
}

func firstElOrZeroVal[T any](slice []T) (val T) {
	if len(slice) == 0 {
		return val
	}
	return slice[0]
}
