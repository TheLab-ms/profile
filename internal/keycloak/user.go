package keycloak

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/Nerzal/gocloak/v13"
)

type User struct {
	First, Last, Email          string
	FobID                       int
	SignedWaiver, ActivePayment bool
	DiscountType                string
	AdminNotes                  string // for leadership only!
	BuildingAccessApproved      bool

	StripeCustomerID      string
	StripeSubscriptionID  string
	StripeCancelationTime int64
	StripeETag            int64

	LastPaypalTransactionPrice float64
	LastPaypalTransactionTime  time.Time
	PaypalSubscriptionID       string
}

func newUser(kcuser *gocloak.User) (*User, error) {
	user := &User{
		First:                  gocloak.PString(kcuser.FirstName),
		Last:                   gocloak.PString(kcuser.LastName),
		Email:                  gocloak.PString(kcuser.Email),
		SignedWaiver:           safeGetAttr(kcuser, "waiverState") == "Signed",
		ActivePayment:          safeGetAttr(kcuser, "stripeID") != "",
		DiscountType:           safeGetAttr(kcuser, "discountType"),
		StripeSubscriptionID:   safeGetAttr(kcuser, "stripeSubscriptionID"),
		BuildingAccessApproved: safeGetAttr(kcuser, "buildingAccessApprover") != "",
		StripeCustomerID:       safeGetAttr(kcuser, "stripeID"),
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

	return user, nil
}
