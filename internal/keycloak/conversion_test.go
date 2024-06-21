package keycloak

import (
	"testing"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/stretchr/testify/assert"
)

func TestConversion(t *testing.T) {
	type testStruct struct {
		Foo string `json:"foo"`
	}

	type testUser struct {
		UUID          string     `keycloak:"id"`
		First         string     `keycloak:"first"`
		Last          string     `keycloak:"last"`
		Email         string     `keycloak:"email"`
		EmailVerified bool       `keycloak:"emailVerified"`
		Str           string     `keycloak:"attr.str"`
		Int           int        `keycloak:"attr.int"`
		BigInt        int64      `keycloak:"attr.int64"`
		Bool          bool       `keycloak:"attr.bool"`
		T             time.Time  `keycloak:"attr.t"`
		Json          testStruct `keycloak:"attr.js"`
	}

	kc := &gocloak.User{
		ID:            gocloak.StringP("test-id"),
		FirstName:     gocloak.StringP("test-first"),
		LastName:      gocloak.StringP("test-last"),
		Email:         gocloak.StringP("test-email"),
		EmailVerified: gocloak.BoolP(true),
		Attributes: &map[string][]string{
			"str":   {"test-str"},
			"int":   {"123"},
			"int64": {"234"},
			"bool":  {"true"},
			"t":     {"123456"},
			"js":    {`{"foo":"bar"}`},
		},
	}

	// To
	user := &testUser{}
	mapToUserType(kc, user)
	assert.Equal(t, &testUser{
		UUID:          "test-id",
		First:         "test-first",
		Last:          "test-last",
		Email:         "test-email",
		EmailVerified: true,
		Str:           "test-str",
		Int:           123,
		BigInt:        234,
		Bool:          true,
		T:             time.Unix(123456, 0),
		Json:          testStruct{Foo: "bar"},
	}, user)

	// From
	copy := &gocloak.User{}
	mapFromUserType(copy, user)
	assert.Equal(t, kc, copy)
}
