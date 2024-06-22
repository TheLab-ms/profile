package keycloak

import (
	"github.com/Nerzal/gocloak/v13"
	"github.com/TheLab-ms/profile/internal/datamodel"
)

// TODO: Replace
func newUser(kcuser *gocloak.User) (*datamodel.User, error) {
	u := &datamodel.User{}
	mapToUserType(kcuser, u)
	return u, nil
}

type ExtendedUser struct {
	User         *datamodel.User
	ActiveMember bool
}
