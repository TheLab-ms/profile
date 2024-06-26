package keycloak

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/Nerzal/gocloak/v13"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/datamodel"
)

var (
	ErrConflict      = errors.New("conflict")
	ErrLimitExceeded = errors.New("limit exceeded")
	ErrNotFound      = errors.New("resource not found")
)

type UserMetadata interface {
}

type ExtendedUser[T UserMetadata] struct {
	User         T
	ActiveMember bool
}

type EventSink interface {
	Eventf(email, reason, templ string, args ...any)
}

type Keycloak[T UserMetadata] struct {
	Sink   EventSink
	client *gocloak.GoCloak
	env    *conf.Env

	// use ensureToken to access these
	tokenLock      sync.Mutex
	token          *gocloak.JWT
	tokenFetchTime time.Time
}

func New[T UserMetadata](c *conf.Env) *Keycloak[T] {
	return &Keycloak[T]{client: gocloak.NewClient(c.KeycloakURL), env: c}
}

// RegisterUser creates a user and initiates the password reset + email confirmation flow.
// Currently the two steps do not occur atomically - we assume the system will not crash between them.
func (k *Keycloak[T]) RegisterUser(ctx context.Context, email string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	n, err := k.client.GetUserCount(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{EmailVerified: gocloak.BoolP(false)})
	if err != nil {
		return fmt.Errorf("counting users with unverified email addresses: %w", err)
	}
	if n > k.env.MaxUnverifiedAccounts {
		k.Sink.Eventf(email, "TooManyUnverified", "refusing to create a new account while there are more than %d accounts with unverified email addresses", k.env.MaxUnverifiedAccounts)
		return ErrLimitExceeded
	}

	_, err = k.client.CreateUser(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.User{
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

	return nil
}

func (k *Keycloak[T]) DeleteUser(ctx context.Context, uuid string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	return k.client.DeleteUser(ctx, token.AccessToken, k.env.KeycloakRealm, uuid)
}

func (k *Keycloak[T]) SendSignupEmail(ctx context.Context, userID string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	clientID, err := k.getClientID()
	if err != nil {
		return err
	}

	resp, err := k.client.GetRequestWithBearerAuth(ctx, token.AccessToken).
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

func (k *Keycloak[T]) GetUser(ctx context.Context, userID string) (T, error) {
	user := k.newUser()
	token, err := k.GetToken(ctx)
	if err != nil {
		return user, fmt.Errorf("getting token: %w", err)
	}

	kcuser, err := k.client.GetUserByID(ctx, token.AccessToken, k.env.KeycloakRealm, userID)
	if err != nil {
		if e, ok := err.(*gocloak.APIError); ok && e.Code == 404 {
			return user, ErrNotFound
		}
		return user, err
	}

	mapToUserType(kcuser, user)
	return user, nil
}

func (k *Keycloak[T]) GetUserByAttribute(ctx context.Context, key, val string) (T, error) {
	user := k.newUser()
	token, err := k.GetToken(ctx)
	if err != nil {
		return user, fmt.Errorf("getting token: %w", err)
	}

	users, err := k.client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
		Q:   gocloak.StringP(fmt.Sprintf("%s:%s", key, val)),
		Max: gocloak.IntP(1),
	})
	if err != nil {
		return user, err
	}
	if len(users) == 0 {
		return user, ErrNotFound
	}

	mapToUserType(users[0], user)
	return user, nil
}

func (k *Keycloak[T]) GetUserByEmail(ctx context.Context, email string) (T, error) {
	user := k.newUser()
	token, err := k.GetToken(ctx)
	if err != nil {
		return user, fmt.Errorf("getting token: %w", err)
	}

	kcusers, err := k.client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
		Email: &email,
	})
	if err != nil {
		return user, fmt.Errorf("getting current user: %w", err)
	}
	if len(kcusers) == 0 {
		return user, ErrNotFound
	}

	mapToUserType(kcusers[0], user)
	return user, nil
}

func (k *Keycloak[T]) WriteUser(ctx context.Context, user *datamodel.User) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcuser := gocloak.User{}
	mapFromUserType(&kcuser, user)
	return k.client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, kcuser)
}

func (k *Keycloak[T]) Deactivate(ctx context.Context, user *datamodel.User) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	return k.client.DeleteUserFromGroup(ctx, token.AccessToken, k.env.KeycloakRealm, user.UUID, k.env.KeycloakMembersGroupID)
}

func (k *Keycloak[T]) UpdateGroupMembership(ctx context.Context, user *datamodel.User, active bool) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	groups, err := k.client.GetUserGroups(ctx, token.AccessToken, k.env.KeycloakRealm, user.UUID, gocloak.GetGroupsParams{
		Search: gocloak.StringP("thelab-members"),
	})
	if err != nil {
		return fmt.Errorf("listing user groups: %w", err)
	}

	// Users should only be in the members group when their Stripe subscription is active
	inGroup := len(groups) > 0
	if !inGroup && active {
		k.Sink.Eventf(user.Email, "MembershipActivated", "A change in payment state caused the user's membership to be enabled")
		err = k.client.AddUserToGroup(ctx, token.AccessToken, k.env.KeycloakRealm, user.UUID, k.env.KeycloakMembersGroupID)
	}
	if inGroup && !active {
		k.Sink.Eventf(user.Email, "MembershipDeactivated", "A change in payment state caused the user's membership to be disabled")
		err = k.client.DeleteUserFromGroup(ctx, token.AccessToken, k.env.KeycloakRealm, user.UUID, k.env.KeycloakMembersGroupID)
	}
	if err != nil {
		return fmt.Errorf("updating user group membership: %w", err)
	}

	return nil
}

func (k *Keycloak[T]) ExtendUser(ctx context.Context, user T, uuid string) (*ExtendedUser[T], error) {
	token, err := k.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	groups, err := k.client.GetUserGroups(ctx, token.AccessToken, k.env.KeycloakRealm, uuid, gocloak.GetGroupsParams{
		Max:    gocloak.IntP(1),
		Search: gocloak.StringP("thelab-members"),
	})
	if err != nil {
		if e, ok := err.(*gocloak.APIError); ok && e.Code == 404 {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("getting group membership: %w", err)
	}

	return &ExtendedUser[T]{
		User:         user,
		ActiveMember: len(groups) > 0,
	}, nil
}

func (k *Keycloak[T]) ListUsers(ctx context.Context) ([]*ExtendedUser[T], error) {
	token, err := k.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	var (
		max           = 150
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
		_, err = k.client.GetRequestWithBearerAuth(ctx, token.AccessToken).
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

	parsedUsers := []*ExtendedUser[T]{}
	max = 150
	first = 0
	for {
		users, err := k.client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{Max: &max, First: &first})
		if err != nil {
			return nil, fmt.Errorf("getting token: %w", err)
		}
		if len(users) == 0 {
			return parsedUsers, nil
		}
		first += len(users)
		for _, kcuser := range users {
			user := k.newUser()
			mapToUserType(kcuser, user)
			_, member := activeMembers[gocloak.PString(kcuser.ID)]
			fullUser := &ExtendedUser[T]{User: user, ActiveMember: member}
			parsedUsers = append(parsedUsers, fullUser)
		}
	}
}

func (k *Keycloak[T]) newUser() (user T) {
	structType := reflect.TypeOf(user).Elem()
	instance := reflect.New(structType).Interface()
	return instance.(T)
}

func safeGetAttrs(kcuser *gocloak.User) map[string][]string {
	if kcuser != nil && kcuser.Attributes != nil {
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
