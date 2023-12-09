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

type Price struct {
	ID, ProductID, ButtonText string
	CouponsByDiscountType     map[string]string
}

// PriceCache is used to store Stripe product prices in-memory to avoid fetching them when rendering pages.
type PriceCache struct {
	mut     sync.Mutex
	state   []*Price
	refresh chan struct{}
}

func (p *PriceCache) Refresh() { p.refresh <- struct{}{} }

func (p *PriceCache) GetPrices() []*Price {
	p.mut.Lock()
	defer p.mut.Unlock()
	return p.state
}

func (p *PriceCache) Start() {
	p.refresh = make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(time.Minute * 15)
		defer ticker.Stop()

		for {
			list := p.listPrices()

			p.mut.Lock()
			p.state = list
			log.Printf("updated cache of %d prices", len(list))
			p.mut.Unlock()

			// Wait until the timer or an explicit refresh
			select {
			case <-ticker.C:
			case <-p.refresh:
			}
		}
	}()
}

func (p *PriceCache) listPrices() []*Price {
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
		if price.Product != nil {
			p.ProductID = price.Product.ID
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
