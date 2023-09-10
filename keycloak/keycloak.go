package keycloak

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/stripe/stripe-go/v75"

	"github.com/TheLab-ms/profile/conf"
)

var (
	ErrConflict = errors.New("conflict")
)

type Keycloak struct {
	client *gocloak.GoCloak
	env    *conf.Env

	// use ensureToken to access these
	tokenLock      sync.Mutex
	token          *gocloak.JWT
	tokenFetchTime time.Time
}

func New(c *conf.Env) *Keycloak {
	return &Keycloak{client: gocloak.NewClient(c.KeycloakURL), env: c}
}

func (k *Keycloak) GetUser(ctx context.Context, userID string) (*User, error) {
	token, err := k.ensureToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	kcuser, err := k.client.GetUserByID(ctx, token.AccessToken, k.env.KeycloakRealm, userID)
	if err != nil {
		return nil, err
	}

	user := &User{
		First:                gocloak.PString(kcuser.FirstName),
		Last:                 gocloak.PString(kcuser.LastName),
		Email:                gocloak.PString(kcuser.Email),
		SignedWaiver:         safeGetAttr(kcuser, "waiverState") == "Signed",
		ActivePayment:        safeGetAttr(kcuser, "stripeID") != "",
		StripeSubscriptionID: safeGetAttr(kcuser, "stripeSubscriptionID"),
	}
	user.StripeCancelationTime, _ = strconv.ParseInt(safeGetAttr(kcuser, "stripeCancelationTime"), 10, 0)
	user.FobID, _ = strconv.Atoi(safeGetAttr(kcuser, "keyfobID"))

	return user, nil
}

func (k *Keycloak) UpdateUserFobID(ctx context.Context, userID string, fobID int) error {
	token, err := k.ensureToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcuser, err := k.client.GetUserByID(ctx, token.AccessToken, k.env.KeycloakRealm, userID)
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}

	attr := safeGetAttrs(kcuser)
	attr["keyfobID"] = []string{strconv.Itoa(fobID)}

	return k.client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
}

func (k *Keycloak) UpdateUserWaiverState(ctx context.Context, email string) error {
	token, err := k.ensureToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcusers, err := k.client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
		Email: &email,
	})
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}
	if len(kcusers) == 0 {
		return errors.New("user not found")
	}
	kcuser := kcusers[0]

	attr := safeGetAttrs(kcuser)
	attr["waiverState"] = []string{"Signed"}

	return k.client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
}

func (k *Keycloak) UpdateUserName(ctx context.Context, userID, first, last string) error {
	token, err := k.ensureToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcuser, err := k.client.GetUserByID(ctx, token.AccessToken, k.env.KeycloakRealm, userID)
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}

	kcuser.FirstName = &first
	kcuser.LastName = &last

	return k.client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
}

func (k *Keycloak) UpdateUserStripeInfo(ctx context.Context, customer *stripe.Customer, sub *stripe.Subscription) error {
	token, err := k.ensureToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcusers, err := k.client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
		Email: &customer.Email,
	})
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}
	if len(kcusers) == 0 {
		return errors.New("user not found")
	}
	kcuser := kcusers[0]

	attr := safeGetAttrs(kcuser)
	active := sub.Status == stripe.SubscriptionStatusActive
	if active {
		attr["stripeID"] = []string{customer.ID}
		attr["stripeSubscriptionID"] = []string{sub.ID}
		attr["stripeCancelationTime"] = []string{strconv.FormatInt(sub.CancelAt, 10)}
	} else {
		attr["stripeID"] = []string{""}
		attr["stripeSubscriptionID"] = []string{""}
		attr["stripeCancelationTime"] = []string{""}
	}

	err = k.client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
	if err != nil {
		return fmt.Errorf("updating user: %w", err)
	}

	groups, err := k.client.GetUserGroups(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, gocloak.GetGroupsParams{
		Search: gocloak.StringP("thelab-members"),
	})
	if err != nil {
		return fmt.Errorf("listing user groups: %w", err)
	}

	// Users should only be in the members group when their Stripe subscription is active
	inGroup := len(groups) > 0
	if !inGroup && active {
		err = k.client.AddUserToGroup(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, k.env.KeycloakMembersGroupID)
	}
	if inGroup && !active {
		err = k.client.DeleteUserFromGroup(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, k.env.KeycloakMembersGroupID)
	}
	if err != nil {
		return fmt.Errorf("updating user group membership: %w", err)
	}

	return nil
}

func (k *Keycloak) RegisterUser(ctx context.Context, email string) error {
	token, err := k.ensureToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	yes := true
	userID, err := k.client.CreateUser(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.User{
		Enabled:  &yes,
		Email:    &email,
		Username: &email,
	})
	if err != nil {
		if e, ok := err.(*gocloak.APIError); ok && e.Code == 404 {
			return ErrConflict
		}
		return fmt.Errorf("creating user: %w", err)
	}

	resp, err := k.client.GetRequestWithBearerAuth(ctx, token.AccessToken).
		SetQueryParams(map[string]string{"lifespan": "43200", "redirect_uri": k.env.SelfURL + "/profile", "client_id": k.env.KeycloakClientID}).
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

// For whatever reason the Keycloak client doesn't support token rotation
func (k *Keycloak) ensureToken(ctx context.Context) (*gocloak.JWT, error) {
	k.tokenLock.Lock()
	defer k.tokenLock.Unlock()

	if k.token != nil && time.Since(k.tokenFetchTime) < (time.Duration(k.token.ExpiresIn)*time.Second)/2 {
		return k.token, nil
	}

	token, err := k.client.LoginAdmin(ctx, k.env.KeycloakUser, k.env.KeycloakPassword, k.env.KeycloakRealm)
	if err != nil {
		return nil, err
	}
	k.token = token
	k.tokenFetchTime = time.Now()

	log.Printf("fetched new auth token from keycloak - will expire in %d seconds", k.token.ExpiresIn)
	return k.token, nil
}

type User struct {
	First, Last, Email          string
	FobID                       int
	SignedWaiver, ActivePayment bool
	StripeSubscriptionID        string
	StripeCancelationTime       int64
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
