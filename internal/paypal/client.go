package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/datamodel"
	"github.com/TheLab-ms/profile/internal/reporting"
)

// Client is a terrible collection of Paypal-related code that has accumulated over time.
// I'm not refactoring it since hopefully we'll get to remove it at some point and much of it isn't really testable.
type Client struct {
	env *conf.Env
}

func NewClient(env *conf.Env) *Client {
	return &Client{env: env}
}

func (c *Client) Cancel(ctx context.Context, user *datamodel.User) error {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.paypal.com/v1/billing/subscriptions/%s", user.PaypalMetadata.TransactionID), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.env.PaypalClientID, c.env.PaypalClientSecret)

	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()

	if getResp.StatusCode == 404 {
		log.Printf("not canceling paypal subscription because it doesn't exist: %s", user.PaypalMetadata.TransactionID)
		return nil
	}
	if getResp.StatusCode > 299 {
		return fmt.Errorf("non-200 response from Paypal when getting subscription: %d", getResp.StatusCode)
	}

	current := struct {
		Status string `json:"status"`
	}{}
	err = json.NewDecoder(getResp.Body).Decode(&current)
	if err != nil {
		return err
	}
	if current.Status == "CANCELLED" {
		log.Printf("not canceling paypal subscription because it's already canceled: %s", user.PaypalMetadata.TransactionID)
		return nil
	}

	body := bytes.NewBufferString(`{ "reason": "migrated account" }`)
	req, err = http.NewRequest("POST", fmt.Sprintf("https://api.paypal.com/v1/billing/subscriptions/%s/cancel", user.PaypalMetadata.TransactionID), body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.env.PaypalClientID, c.env.PaypalClientSecret)
	req.Header.Set("Content-Type", "application/json")

	postResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()

	if postResp.StatusCode == 404 {
		log.Printf("not canceling paypal subscription because it doesn't exist even after previous check: %s", user.PaypalMetadata.TransactionID)
		return nil
	}
	if postResp.StatusCode > 299 {
		body, _ := io.ReadAll(postResp.Body)
		return fmt.Errorf("non-200 response from Paypal when canceling: %d - %s", postResp.StatusCode, body)
	}

	log.Printf("canceled paypal subscription: %s", user.PaypalMetadata.TransactionID)
	reporting.DefaultSink.Eventf(user.Email, "CanceledPaypal", "Successfully migrated user off of paypal")
	return nil
}

func (c *Client) GetSubscription(ctx context.Context, id string) (*Subscription, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.paypal.com/v1/billing/subscriptions/%s", id), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.env.PaypalClientID, c.env.PaypalClientSecret)

	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer getResp.Body.Close()

	if getResp.StatusCode == 404 {
		return nil, nil
	}
	if getResp.StatusCode > 299 {
		body, _ := io.ReadAll(getResp.Body)
		return nil, fmt.Errorf("error response %d from Paypal when getting subscription: %s", getResp.StatusCode, body)
	}

	current := &Subscription{}
	err = json.NewDecoder(getResp.Body).Decode(&current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

type Subscription struct {
	Status  string      `json:"status"`
	Billing BillingInfo `json:"billing_info"`
}

type BillingInfo struct {
	LastPayment Payment `json:"last_payment"`
}

type Payment struct {
	Amount Amount    `json:"amount"`
	Time   time.Time `json:"time"`
}

type Amount struct {
	Value string `json:"value"`
}
