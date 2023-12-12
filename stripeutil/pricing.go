package stripeutil

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v75"
	"github.com/stripe/stripe-go/v75/coupon"
	"github.com/stripe/stripe-go/v75/price"
	"github.com/stripe/stripe-go/v75/product"
)

type Price struct {
	ID, ProductID         string
	Annual                bool
	Price                 float64
	CouponsByDiscountType map[string]string
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
	// Discover product ID
	products := product.Search(&stripe.ProductSearchParams{
		SearchParams: stripe.SearchParams{
			Query: `name:"Membership"`,
		},
	})
	products.Next()
	product := products.Product()
	if product == nil {
		return nil // TODO: Retry errors to avoid running with an empty cache (or maybe just fail liveness probes)
	}

	// Coupons
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

	// Prices
	prices := price.List(&stripe.PriceListParams{
		Active:  stripe.Bool(true),
		Type:    stripe.String("recurring"),
		Product: &product.ID,
	})
	returns := []*Price{}
	for prices.Next() {
		price := prices.Price()
		if price.Metadata == nil || price.Recurring == nil || !price.Active || price.Deleted {
			continue
		}
		p := &Price{
			ID:                    price.ID,
			CouponsByDiscountType: coupsMap[price.ID],
			Price:                 price.UnitAmountDecimal / 100,
		}
		switch price.Recurring.Interval {
		case stripe.PriceRecurringIntervalMonth:
		case stripe.PriceRecurringIntervalYear:
			p.Annual = true
		default:
			continue
		}
		if price.Product != nil {
			p.ProductID = price.Product.ID
		}
		returns = append(returns, p)
	}

	return returns
}
