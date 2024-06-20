package server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/payment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO: Write more tests

func TestRenderProfile(t *testing.T) {
	tests := []struct {
		Name    string
		Fixture string
		User    *keycloak.User
	}{
		{
			Name:    "basic stripe member",
			Fixture: "basic.html",
			User: &keycloak.User{
				First:         "Steve",
				Last:          "Ballmer",
				FobID:         666,
				EmailVerified: true,
				SignedWaiver:  true,
				Email:         "developers@microsoft.com",
				DiscountType:  "developersdevelopersdevelopers",
			},
		},
		{
			Name:    "paypal member",
			Fixture: "paypal.html",
			User: &keycloak.User{
				First:                      "Steve",
				Last:                       "Ballmer",
				FobID:                      666,
				EmailVerified:              true,
				SignedWaiver:               true,
				Email:                      "developers@microsoft.com",
				DiscountType:               "developersdevelopersdevelopers",
				LastPaypalTransactionTime:  time.Now(),
				LastPaypalTransactionPrice: 6000,
				PaypalSubscriptionID:       "foobarbaz",
			},
		},
		{
			Name:    "non-billable member",
			Fixture: "nonbillable.html",
			User: &keycloak.User{
				First:         "Steve",
				Last:          "Ballmer",
				FobID:         666,
				EmailVerified: true,
				SignedWaiver:  true,
				Email:         "developers@microsoft.com",
				DiscountType:  "developersdevelopersdevelopers",
				NonBillable:   true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			prices := []*payment.Price{{ID: "foo", Price: 1000}}
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
