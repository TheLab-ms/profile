package stripeutil

import (
	"log"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v75"
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
	ID, ButtonText string
}

func ListPrices() []*Price {
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
			ID:         price.ID,
			ButtonText: price.Metadata["ButtonText"],
		}
		if p.ButtonText == "" {
			continue // skip prices that don't have button text
		}
		returns = append(returns, p)
	}

	return returns
}
