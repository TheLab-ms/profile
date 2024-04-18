package payment

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

// PriceCache is used to store Stripe product prices in-memory to avoid fetching them when rendering pages.
type PriceCache struct {
	mut     sync.Mutex
	state   *cacheState
	refresh chan struct{}
}

func (p *PriceCache) Refresh() { p.refresh <- struct{}{} }

func (p *PriceCache) GetPrices() []*Price {
	p.mut.Lock()
	defer p.mut.Unlock()
	if p.state == nil {
		return nil
	}
	return p.state.Prices
}

func (p *PriceCache) GetDiscountTypes() []string {
	p.mut.Lock()
	defer p.mut.Unlock()
	if p.state == nil {
		return nil
	}
	return p.state.DiscountTypes
}

func (p *PriceCache) Start() {
	p.refresh = make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(time.Minute * 15)
		defer ticker.Stop()

		for {
			state := p.listPrices()
			if len(state.Prices) == 0 || state == nil {
				time.Sleep(time.Second)
				log.Printf("failed to populate Stripe cache - will retry")
				continue
			}

			p.mut.Lock()
			p.state = state
			log.Printf("updated cache of %d prices", len(state.Prices))
			p.mut.Unlock()

			// Wait until the timer or an explicit refresh
			select {
			case <-ticker.C:
			case <-p.refresh:
			}
		}
	}()
}

func (p *PriceCache) listPrices() *cacheState {
	// Discover product ID
	products := product.Search(&stripe.ProductSearchParams{
		SearchParams: stripe.SearchParams{
			Query: `name:"Membership"`,
		},
	})
	products.Next()
	product := products.Product()
	if product == nil {
		// the stripe library logs errors - no need to do so here
		return nil
	}

	// Coupons
	coupons := coupon.List(&stripe.CouponListParams{})
	coupsIDs := map[string]map[string]string{}      // mapping of price ID -> discount type -> coupon ID
	coupsAmountOff := map[string]map[string]int64{} // mapping of price ID -> discount type -> discount
	allDiscountTypes := []string{}
	for coupons.Next() {
		coup := coupons.Coupon()
		if coup.Metadata == nil || coup.Metadata["priceID"] == "" || coup.Metadata["discountTypes"] == "" {
			continue
		}
		priceID := coup.Metadata["priceID"]
		discountTypes := strings.Split(coup.Metadata["discountTypes"], ",")
		allDiscountTypes = append(allDiscountTypes, discountTypes...)
		if coupsIDs[priceID] == nil {
			coupsIDs[priceID] = map[string]string{}
		}
		if coupsAmountOff[priceID] == nil {
			coupsAmountOff[priceID] = map[string]int64{}
		}
		for _, dt := range discountTypes {
			coupsIDs[priceID][dt] = coup.ID
			coupsAmountOff[priceID][dt] = coup.AmountOff
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
			ID:               price.ID,
			CouponIDs:        coupsIDs[price.ID],
			CouponAmountsOff: coupsAmountOff[price.ID],
			Price:            price.UnitAmountDecimal / 100,
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

	return &cacheState{
		Prices:        returns,
		DiscountTypes: allDiscountTypes,
	}
}

type Price struct {
	ID, ProductID    string
	Annual           bool
	Price            float64
	CouponIDs        map[string]string
	CouponAmountsOff map[string]int64
}

type cacheState struct {
	Prices        []*Price
	DiscountTypes []string
}
