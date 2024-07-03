package server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO: Test without signed waiver, verified email, name, etc.

func TestRenderProfile(t *testing.T) {
	tests := []struct {
		Name    string
		Fixture string
		User    *datamodel.User
	}{
		{
			Name:    "basic stripe member",
			Fixture: "basic.html",
			User: &datamodel.User{
				First:         "Steve",
				Last:          "Ballmer",
				FobID:         666,
				EmailVerified: true,
				WaiverState:   "Signed",
				Email:         "developers@microsoft.com",
				DiscountType:  "developersdevelopersdevelopers",
			},
		},
		{
			Name:    "no payment yet",
			Fixture: "inactive.html",
			User: &datamodel.User{
				First:         "Steve",
				Last:          "Ballmer",
				FobID:         666,
				EmailVerified: true,
				WaiverState:   "Signed",
				Email:         "developers@microsoft.com",
			},
		},
		{
			Name:    "canceled stripe member",
			Fixture: "canceled.html",
			User: &datamodel.User{
				First:                 "Steve",
				Last:                  "Ballmer",
				FobID:                 666,
				EmailVerified:         true,
				WaiverState:           "Signed",
				Email:                 "developers@microsoft.com",
				DiscountType:          "developersdevelopersdevelopers",
				StripeSubscriptionID:  "foo",
				StripeCancelationTime: time.Unix(100000, 0).UTC().Add(-time.Hour),
			},
		},
		{
			Name:    "paypal member",
			Fixture: "paypal.html",
			User: &datamodel.User{
				First:         "Steve",
				Last:          "Ballmer",
				FobID:         666,
				EmailVerified: true,
				WaiverState:   "Signed",
				Email:         "developers@microsoft.com",
				DiscountType:  "developersdevelopersdevelopers",
				PaypalMetadata: datamodel.PaypalMetadata{
					Price:         6000,
					TimeRFC3339:   time.Unix(100000, 0).UTC().Add(-time.Hour),
					TransactionID: "foobarbaz",
				},
			},
		},
		{
			Name:    "non-billable member",
			Fixture: "nonbillable.html",
			User: &datamodel.User{
				First:         "Steve",
				Last:          "Ballmer",
				FobID:         666,
				EmailVerified: true,
				WaiverState:   "Signed",
				Email:         "developers@microsoft.com",
				DiscountType:  "developersdevelopersdevelopers",
				NonBillable:   true,
			},
		},
		{
			Name:    "deactivated member",
			Fixture: "deactivated.html",
			User: &datamodel.User{
				First:         "Steve",
				Last:          "Ballmer",
				EmailVerified: true,
				WaiverState:   "Signed",
				Email:         "developers@microsoft.com",
				DiscountType:  "developersdevelopersdevelopers",
				NonBillable:   true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			prices := []*datamodel.PriceDetails{{ID: "foo", Price: 1000}}
			buf := &bytes.Buffer{}
			err := renderProfile(buf, test.User, prices)
			require.NoError(t, err)

			fp := filepath.Join("fixtures", test.Fixture)
			if os.Getenv("GENERATE_FIXTURES") != "" {
				err = os.WriteFile(fp, buf.Bytes(), 0644)
				require.NoError(t, err)
				return
			}

			fixture, err := os.ReadFile(fp)
			require.NoError(t, err)
			assert.Equal(t, string(fixture), buf.String())
		})
	}
}
