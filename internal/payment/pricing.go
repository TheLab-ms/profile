package payment

import (
	"context"
	"time"

	"github.com/stripe/stripe-go/v78"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/datamodel"
)

// NewCheckoutSessionParams sets the various Stripe checkout options for a new registering member.
func NewCheckoutSessionParams(ctx context.Context, user *datamodel.User, env *conf.Env, pc *PriceCache, priceID string) *stripe.CheckoutSessionParams {
	checkoutParams := &stripe.CheckoutSessionParams{
		Mode:       stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		SuccessURL: stripe.String(env.SelfURL + "/profile"),
		CancelURL:  stripe.String(env.SelfURL + "/profile"),
	}
	if user.StripeCustomerID == "" {
		checkoutParams.CustomerEmail = &user.Email
	} else {
		checkoutParams.Customer = &user.StripeCustomerID
	}
	checkoutParams.Context = ctx

	// Calculate specific pricing based on the member's profile
	checkoutParams.LineItems = calculateLineItems(user, priceID, pc)
	checkoutParams.Discounts = calculateDiscount(user, priceID, pc)
	if checkoutParams.Discounts == nil {
		// Stripe API doesn't allow Discounts and AllowPromotionCodes to be set
		checkoutParams.AllowPromotionCodes = stripe.Bool(true)
	}

	checkoutParams.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{
		BillingCycleAnchor: calculateBillingCycleAnchor(user), // This enables migration from paypal
	}
	if checkoutParams.SubscriptionData.BillingCycleAnchor != nil {
		// In this case, the member is already paid up - don't make them pay for the currenet period again
		checkoutParams.SubscriptionData.ProrationBehavior = stripe.String("none")
	}
	return checkoutParams
}

func calculateLineItems(user *datamodel.User, priceID string, pc *PriceCache) []*stripe.CheckoutSessionLineItemParams {
	// Migrate existing paypal users at their current rate
	if priceID == "paypal" {
		interval := "month"
		if user.PaypalMetadata.Price > 50 {
			interval = "year"
		}

		cents := user.PaypalMetadata.Price * 100
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

func calculateDiscount(user *datamodel.User, priceID string, pc *PriceCache) []*stripe.CheckoutSessionDiscountParams {
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

func calculateBillingCycleAnchor(user *datamodel.User) *int64 {
	if user.PaypalMetadata.Price == 0 {
		return nil
	}

	var end time.Time
	if user.PaypalMetadata.Price > 41 {
		// Annual
		end = user.PaypalMetadata.TimeRFC3339.Add(time.Hour * 24 * 365)
	} else {
		// Monthly
		end = user.PaypalMetadata.TimeRFC3339.Add(time.Hour * 24 * 30)
	}

	// Stripe will throw an error if the cycle anchor is before the current time
	if time.Until(end) < time.Minute {
		return nil
	}

	ts := end.Unix()
	return &ts
}

func CalculateDiscounts(user *datamodel.User, prices []*datamodel.PriceDetails) []*datamodel.PriceDetails {
	if user.DiscountType == "" {
		return prices
	}
	out := make([]*datamodel.PriceDetails, len(prices))
	for i, price := range prices {
		amountOff := price.CouponAmountsOff[user.DiscountType]
		out[i] = &datamodel.PriceDetails{
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
