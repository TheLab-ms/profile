package keycloak

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
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

func mapToUserType(kcuser *gocloak.User, user any) {
	rt := reflect.TypeOf(user).Elem()
	rv := reflect.ValueOf(user).Elem()

	for i := 0; i < rv.NumField(); i++ {
		ft := rt.Field(i)
		fv := rv.Field(i)
		tag := ft.Tag.Get("keycloak")
		if tag == "id" {
			fv.SetString(gocloak.PString(kcuser.ID))
		} else if tag == "first" {
			fv.SetString(gocloak.PString(kcuser.FirstName))
		} else if tag == "last" {
			fv.SetString(gocloak.PString(kcuser.LastName))
		} else if tag == "email" {
			fv.SetString(gocloak.PString(kcuser.Email))
		} else if tag == "emailVerified" {
			fv.SetBool(gocloak.PBool(kcuser.EmailVerified))
		}
		if !strings.HasPrefix(tag, "attr.") {
			continue
		}

		key := strings.TrimPrefix(tag, "attr.")
		val := safeGetAttr(kcuser, key)
		if val == "" {
			continue
		}
		tn := rv.Field(i).Type().String()
		switch tn {
		case "int", "int64":
			i, _ := strconv.ParseInt(val, 10, 0)
			fv.SetInt(i)
		case "bool":
			b, _ := strconv.ParseBool(val)
			fv.SetBool(b)
		case "string":
			fv.SetString(val)
		case "time.Time":
			i, _ := strconv.ParseInt(val, 10, 0)
			t := time.Unix(i, 0)
			fv.Set(reflect.ValueOf(t))
		default:
			v := reflect.New(ft.Type).Interface()
			json.Unmarshal([]byte(val), &v)
			fv.Set(reflect.ValueOf(v).Elem())
		}
	}
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
