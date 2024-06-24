package keycloak

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Nerzal/gocloak/v13"
)

// For whatever reason the Keycloak client doesn't support token rotation
func (k *Keycloak[T]) GetToken(ctx context.Context) (*gocloak.JWT, error) {
	k.tokenLock.Lock()
	defer k.tokenLock.Unlock()

	if k.token != nil && time.Since(k.tokenFetchTime) < (time.Duration(k.token.ExpiresIn)*time.Second)/2 {
		return k.token, nil
	}

	clientID, err := k.getClientID()
	if err != nil {
		return nil, err
	}
	clientSecret, err := k.getClientSecret()
	if err != nil {
		return nil, err
	}

	token, err := k.client.LoginClient(ctx, string(clientID), string(clientSecret), k.env.KeycloakRealm)
	if err != nil {
		return nil, err
	}
	k.token = token
	k.tokenFetchTime = time.Now()

	log.Printf("fetched new auth token from keycloak - will expire in %d seconds", k.token.ExpiresIn)
	return k.token, nil
}

func (k *Keycloak[T]) getClientID() (string, error) {
	if len(k.env.KeycloakClientID) > 0 {
		return k.env.KeycloakClientID, nil
	}
	clientID, err := os.ReadFile("/var/lib/keycloak/client-id")
	if err != nil {
		return "", fmt.Errorf("reading client id from disk: %w", err)
	}
	return string(clientID), nil
}

func (k *Keycloak[T]) getClientSecret() (string, error) {
	if len(k.env.KeycloakClientSecret) > 0 {
		return k.env.KeycloakClientSecret, nil
	}
	clientSecret, err := os.ReadFile("/var/lib/keycloak/client-secret")
	if err != nil {
		return "", fmt.Errorf("reading client secret from disk: %w", err)
	}
	return string(clientSecret), nil
}
