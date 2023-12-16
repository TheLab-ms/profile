package main

import (
	"time"

	"github.com/stripe/stripe-go/v75"

	"github.com/TheLab-ms/profile/internal/keycloak"
	"github.com/TheLab-ms/profile/internal/stripeutil"
)

func calculateLineItems(user *keycloak.User, priceID string, pc *stripeutil.PriceCache) []*stripe.CheckoutSessionLineItemParams {
	// Migrate existing paypal users at their current rate
	if priceID == "paypal" {
		interval := "month"
		if user.LastPaypalTransactionPrice > 50 {
			interval = "year"
		}

		cents := user.LastPaypalTransactionPrice * 100
		productID := pc.GetPrices()[0].ProductID // all prices reference the same product
		return []*stripe.CheckoutSessionLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:          stripe.String("usd"),
				Product:           &productID,
				UnitAmountDecimal: &cents,
				Recurring: &stripe.CheckoutSessionLineItemPriceDataRecurringParams{
					Interval: &interval,
				},
			},
		}}
	}

	return []*stripe.CheckoutSessionLineItemParams{{
		Price:    stripe.String(priceID),
		Quantity: stripe.Int64(1),
	}}
}

func calculateDiscount(user *keycloak.User, priceID string, pc *stripeutil.PriceCache) []*stripe.CheckoutSessionDiscountParams {
	if user.DiscountType == "" || priceID == "" {
		return nil
	}
	for _, price := range pc.GetPrices() {
		if price.ID == priceID && price.CouponIDs != nil && price.CouponIDs[user.DiscountType] != "" {
			return []*stripe.CheckoutSessionDiscountParams{{
				Coupon: stripe.String(price.CouponIDs[user.DiscountType]),
			}}
		}
	}
	return nil
}

func calculateDiscounts(user *keycloak.User, prices []*stripeutil.Price) []*stripeutil.Price {
	if user.DiscountType == "" {
		return prices
	}
	out := make([]*stripeutil.Price, len(prices))
	for i, price := range prices {
		amountOff := price.CouponAmountsOff[user.DiscountType]
		out[i] = &stripeutil.Price{
			ID:               price.ID,
			ProductID:        price.ProductID,
			Annual:           price.Annual,
			Price:            price.Price - (float64(amountOff) / 100),
			CouponIDs:        price.CouponIDs,
			CouponAmountsOff: price.CouponAmountsOff,
		}
	}
	return out
}

func calculateBillingCycleAnchor(user *keycloak.User) *int64 {
	if user.LastPaypalTransactionPrice == 0 {
		return nil
	}

	var end time.Time
	if user.LastPaypalTransactionPrice > 41 {
		// Annual
		end = user.LastPaypalTransactionTime.Add(time.Hour * 24 * 365)
	} else {
		// Monthly
		end = user.LastPaypalTransactionTime.Add(time.Hour * 24 * 30)
	}

	// Stripe will throw an error if the cycle anchor is before the current time
	if time.Until(end) < time.Minute {
		return nil
	}

	ts := end.Unix()
	return &ts
}
