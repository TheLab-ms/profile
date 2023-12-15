package keycloak

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/stripe/stripe-go/v75"

	"github.com/TheLab-ms/profile/conf"
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

	resp, err := k.Client.GetRequestWithBearerAuth(ctx, token.AccessToken).
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

	return k.buildUser(ctx, kcuser)
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

	return k.buildUser(ctx, kcusers[0])
}

func (k *Keycloak) buildUser(ctx context.Context, kcuser *gocloak.User) (*User, error) {
	user := &User{
		First:                  gocloak.PString(kcuser.FirstName),
		Last:                   gocloak.PString(kcuser.LastName),
		Email:                  gocloak.PString(kcuser.Email),
		SignedWaiver:           safeGetAttr(kcuser, "waiverState") == "Signed",
		ActivePayment:          safeGetAttr(kcuser, "stripeID") != "",
		DiscountType:           safeGetAttr(kcuser, "discountType"),
		StripeSubscriptionID:   safeGetAttr(kcuser, "stripeSubscriptionID"),
		BuildingAccessApproved: safeGetAttr(kcuser, "buildingAccessApprover") != "",
	}
	user.FobID, _ = strconv.Atoi(safeGetAttr(kcuser, "keyfobID"))
	user.StripeCancelationTime, _ = strconv.ParseInt(safeGetAttr(kcuser, "stripeCancelationTime"), 10, 0)
	user.StripeETag, _ = strconv.ParseInt(safeGetAttr(kcuser, "stripeETag"), 10, 0)

	if js := safeGetAttr(kcuser, "paypalMigrationMetadata"); js != "" {
		s := struct {
			Price         float64
			TimeRFC3339   string
			TransactionID string
		}{}
		err := json.Unmarshal([]byte(js), &s)
		if err != nil {
			return nil, err
		}

		user.LastPaypalTransactionPrice = s.Price
		user.PaypalSubscriptionID = s.TransactionID
		user.LastPaypalTransactionTime, err = time.Parse(time.RFC3339, s.TimeRFC3339)
		if err != nil {
			return nil, err
		}
	}

	token, err := k.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	kuserlogins, err := k.Client.GetUserFederatedIdentities(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID)
	if err != nil {
		return nil, err
	}

	for _, login := range kuserlogins {
		if *login.IdentityProvider == "discord" {
			user.DiscordLinked = true
		}
	}

	return user, nil
}

func (k *Keycloak) UpdateUserFobID(ctx context.Context, userID string, fobID int) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcuser, err := k.Client.GetUserByID(ctx, token.AccessToken, k.env.KeycloakRealm, userID)
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}

	attr := safeGetAttrs(kcuser)
	if fobID == 0 {
		attr["keyfobID"] = []string{""}
	} else {
		attr["keyfobID"] = []string{strconv.Itoa(fobID)}
	}

	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
}

func (k *Keycloak) UpdateUserWaiverState(ctx context.Context, email string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcusers, err := k.Client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
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

	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
}

func (k *Keycloak) UpdateUserName(ctx context.Context, userID, first, last string) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcuser, err := k.Client.GetUserByID(ctx, token.AccessToken, k.env.KeycloakRealm, userID)
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}

	kcuser.FirstName = &first
	kcuser.LastName = &last

	return k.Client.UpdateUser(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser)
}

func (k *Keycloak) UpdateUserStripeInfo(ctx context.Context, customer *stripe.Customer, sub *stripe.Subscription) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	kcusers, err := k.Client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{
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

	// Don't de-activate accounts when we receive cancelation webhooks for a subscription that is not currently in use.
	// This shouldn't be possible for any accounts other than tests.
	if !active && !strings.EqualFold(safeGetAttr(kcuser, "stripeSubscriptionID"), sub.ID) {
		log.Printf("dropping cancelation webhook for user %s because the subscription ID doesn't match the one in keycloak", *kcuser.Email)
		return nil
	}

	// Always clean up any old paypal metadata
	attr["paypalMigrationMetadata"] = []string{}

	if active {
		attr["stripeID"] = []string{customer.ID}
		attr["stripeSubscriptionID"] = []string{sub.ID}
		attr["stripeCancelationTime"] = []string{strconv.FormatInt(sub.CancelAt, 10)}
	} else {
		attr["stripeID"] = []string{""}
		attr["stripeSubscriptionID"] = []string{""}
		attr["stripeCancelationTime"] = []string{""}
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
		err = k.Client.AddUserToGroup(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, k.env.KeycloakMembersGroupID)
	}
	if inGroup && !active {
		err = k.Client.DeleteUserFromGroup(ctx, token.AccessToken, k.env.KeycloakRealm, *kcuser.ID, k.env.KeycloakMembersGroupID)
	}
	if err != nil {
		return fmt.Errorf("updating user group membership: %w", err)
	}

	return nil
}

func (k *Keycloak) DumpUsers(ctx context.Context, w io.Writer) error {
	token, err := k.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	cw := csv.NewWriter(w)
	cw.Write([]string{
		"First",
		"Last",
		"Email",
		"Email Verified",
		"Waiver Signed",
		"Stripe ID",
		"Stripe Subscription ID",
		"Discount Type",
		"Keyfob ID",
		"Active Member",
		"Signup Timestamp",
	})

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
			return err
		}

		// Unfortunately the keycloak client doesn't support the group membership endpoint.
		// We reuse the client's transport here while specifying our own URL.
		var memberships []*gocloak.User
		_, err = k.Client.GetRequestWithBearerAuth(ctx, token.AccessToken).
			SetResult(&memberships).
			SetQueryParams(params).
			Get(fmt.Sprintf("%s/admin/realms/%s/groups/%s/members", k.env.KeycloakURL, k.env.KeycloakRealm, k.env.KeycloakMembersGroupID))
		if err != nil {
			return err
		}
		if len(memberships) == 0 {
			break
		}
		first += len(memberships)

		for _, member := range memberships {
			activeMembers[gocloak.PString(member.ID)] = struct{}{}
		}
	}

	max = 50
	first = 0
	for {
		users, err := k.Client.GetUsers(ctx, token.AccessToken, k.env.KeycloakRealm, gocloak.GetUsersParams{Max: &max, First: &first})
		if err != nil {
			return fmt.Errorf("getting token: %w", err)
		}
		if len(users) == 0 {
			return nil
		}
		first += len(users)
		for _, user := range users {
			_, member := activeMembers[gocloak.PString(user.ID)]
			var signupTimeStr string
			signupTime, _ := strconv.Atoi(safeGetAttr(user, "signupEpochTimeUTC"))
			if signupTime > 0 {
				signupTimeStr = time.Unix(int64(signupTime), 0).Local().Format(time.RFC3339)
			}
			cw.Write([]string{
				gocloak.PString(user.FirstName),
				gocloak.PString(user.LastName),
				gocloak.PString(user.Email),
				strconv.FormatBool(gocloak.PBool(user.EmailVerified)),
				strconv.FormatBool(safeGetAttr(user, "waiverState") == "Signed"),
				safeGetAttr(user, "stripeID"),
				safeGetAttr(user, "stripeSubscriptionID"),
				safeGetAttr(user, "discountType"),
				safeGetAttr(user, "keyfobID"),
				strconv.FormatBool(member),
				signupTimeStr,
			})
		}
		cw.Flush() // avoid buffering entire response in memory
	}
}

// For whatever reason the Keycloak client doesn't support token rotation
func (k *Keycloak) GetToken(ctx context.Context) (*gocloak.JWT, error) {
	k.tokenLock.Lock()
	defer k.tokenLock.Unlock()

	if k.token != nil && time.Since(k.tokenFetchTime) < (time.Duration(k.token.ExpiresIn)*time.Second)/2 {
		return k.token, nil
	}

	token, err := k.Client.LoginAdmin(ctx, k.env.KeycloakUser, k.env.KeycloakPassword, k.env.KeycloakRealm)
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
	DiscountType                string
	DiscordLinked               bool
	AdminNotes                  string // for leadership only!
	BuildingAccessApproved      bool

	StripeSubscriptionID  string
	StripeCancelationTime int64
	StripeETag            int64

	LastPaypalTransactionPrice float64
	LastPaypalTransactionTime  time.Time
	PaypalSubscriptionID       string
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
