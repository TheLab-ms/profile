package payment

import (
	"context"
	"strconv"
	"time"

	"github.com/stripe/stripe-go/v75"

	"github.com/TheLab-ms/profile/internal/conf"
	"github.com/TheLab-ms/profile/internal/keycloak"
)

// NewCheckoutSessionParams sets the various Stripe checkout options for a new registering member.
func NewCheckoutSessionParams(ctx context.Context, user *keycloak.User, env *conf.Env, pc *PriceCache, priceID string) *stripe.CheckoutSessionParams {
	etag := strconv.FormatInt(user.StripeETag+1, 10)
	checkoutParams := &stripe.CheckoutSessionParams{
		Mode:          stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		CustomerEmail: &user.Email,
		SuccessURL:    stripe.String(env.SelfURL + "/profile?i=" + etag),
		CancelURL:     stripe.String(env.SelfURL + "/profile"),
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
		Metadata:           map[string]string{"etag": etag},
		BillingCycleAnchor: calculateBillingCycleAnchor(user), // This enables migration from paypal
	}
	if checkoutParams.SubscriptionData.BillingCycleAnchor != nil {
		// In this case, the member is already paid up - don't make them pay for the currenet period again
		checkoutParams.SubscriptionData.ProrationBehavior = stripe.String("none")
	}
	return checkoutParams
}

func calculateLineItems(user *keycloak.User, priceID string, pc *PriceCache) []*stripe.CheckoutSessionLineItemParams {
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

func calculateDiscount(user *keycloak.User, priceID string, pc *PriceCache) []*stripe.CheckoutSessionDiscountParams {
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

func CalculateDiscounts(user *keycloak.User, prices []*Price) []*Price {
	if user.DiscountType == "" {
		return prices
	}
	out := make([]*Price, len(prices))
	for i, price := range prices {
		amountOff := price.CouponAmountsOff[user.DiscountType]
		out[i] = &Price{
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