package keycloak

import (
	"time"

	"github.com/Nerzal/gocloak/v13"
)

type PaypalMetadata struct {
	Price         float64
	TimeRFC3339   time.Time
	TransactionID string
}

type User struct {
	PaypalMetadata         `keycloak:"attr.paypalMigrationMetadata"`
	UUID                   string    `keycloak:"id"`
	Username               string    `keycloak:"username"`
	First                  string    `keycloak:"first"`
	Last                   string    `keycloak:"last"`
	Email                  string    `keycloak:"email"`
	EmailVerified          bool      `keycloak:"emailVerified"`
	FobID                  int       `keycloak:"attr.fobID"`
	WaiverState            string    `keycloak:"attr.waiverState"`
	NonBillable            bool      `keycloak:"attr.nonBillable"`
	DiscountType           string    `keycloak:"attr.discountType"`
	BuildingAccessApprover string    `keycloak:"attr.buildingAccessApprover"`
	SignupTime             time.Time `keycloak:"attr.signupEpochTimeUTC"`
	LastSwipeTime          time.Time `keycloak:"attr.lastSwipeTime"`
	DiscordUserID          int64     `keycloak:"attr.discordUserID"`

	StripeCustomerID      string    `keycloak:"attr.stripeID"`
	StripeSubscriptionID  string    `keycloak:"attr.stripeSubscriptionID"`
	StripeCancelationTime time.Time `keycloak:"attr.stripeCancelationTime"`

	keycloakObject *gocloak.User
}

// TODO: Replace
func newUser(kcuser *gocloak.User) (*User, error) {
	u := &User{}
	mapToUserType(kcuser, u)
	u.keycloakObject = kcuser
	return u, nil
}

type ExtendedUser struct {
	*User
	ActiveMember bool
}

func (u *User) PaymentStatus() string {
	if u.NonBillable {
		return "NonBillable"
	}
	if u.StripeSubscriptionID != "" {
		return "StripeActive"
	}
	if u.PaypalMetadata.TransactionID != "" {
		return "Paypal"
	}
	return "InactiveOrUnknown"
}
