package stripeutil

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v75"
	"github.com/stripe/stripe-go/v75/coupon"
	"github.com/stripe/stripe-go/v75/price"
)

// PriceCache is used to store Stripe product prices in-memory to avoid fetching them when rendering pages.
type PriceCache func() []*Price

func StartPriceCache() PriceCache {
	var mut sync.Mutex
	state := []*Price{}

	go func() {
		for {
			list := ListPrices()

			mut.Lock()
			state = list
			log.Printf("updated cache of %d prices", len(list))
			mut.Unlock()

			time.Sleep(time.Minute * 15)
		}
	}()

	return func() []*Price {
		mut.Lock()
		defer mut.Unlock()
		return state
	}
}

type Price struct {
	ID, ButtonText        string
	CouponsByDiscountType map[string]string
}

func ListPrices() []*Price {
	coupons := coupon.List(&stripe.CouponListParams{})
	coupsMap := map[string]map[string]string{} // mapping of price ID -> discount type -> coupon ID
	for coupons.Next() {
		coup := coupons.Coupon()
		if coup.Metadata == nil || coup.Metadata["priceID"] == "" || coup.Metadata["discountTypes"] == "" {
			continue
		}
		priceID := coup.Metadata["priceID"]
		discountTypes := strings.Split(coup.Metadata["discountTypes"], ",")
		if coupsMap[priceID] == nil {
			coupsMap[priceID] = map[string]string{}
		}
		for _, dt := range discountTypes {
			coupsMap[priceID][dt] = coup.ID
		}
	}

	params := &stripe.PriceListParams{
		Active: stripe.Bool(true),
	}

	prices := price.List(params)
	returns := []*Price{}
	for prices.Next() {
		price := prices.Price()
		if price.Metadata == nil {
			continue
		}
		p := &Price{
			ID:                    price.ID,
			ButtonText:            price.Metadata["ButtonText"],
			CouponsByDiscountType: coupsMap[price.ID],
		}
		if p.ButtonText == "" {
			continue // skip prices that don't have button text
		}
		returns = append(returns, p)
	}

	if os.Getenv("DEV") != "" {
		js, _ := json.MarshalIndent(&returns, "  ", "  ")
		log.Printf("discovered prices from stripe: %s", js)
	}

	return returns
}
