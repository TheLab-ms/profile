package datamodel

import (
	"github.com/TheLab-ms/profile/internal/payment"
)

type Prices struct {
	Yearly  Price `json:"yearly"`
	Monthly Price `json:"monthly"`
}

func NewPrices(items []*payment.Price) *Prices {
	// Pick the first yearly and monthly price that we find in the cache
	// based on no particular order (since we expect to only have one of each)
	var yearly, monthly *payment.Price
	for _, price := range items {
		price := price // GO WHY ARE YOU THIS WAY

		if price.Annual && yearly == nil {
			yearly = price
		}
		if !price.Annual && monthly == nil {
			monthly = price
		}
		if monthly != nil && yearly != nil {
			break
		}
	}

	// Convert to our API response types
	// When selecting discounts, pick the smallest i.e. most expensive.
	prices := &Prices{}
	if yearly != nil {
		prices.Yearly.Price = yearly.Price

		var discount float64
		for _, c := range yearly.CouponAmountsOff {
			fd := float64(c) / 100
			if fd < discount || discount == 0 {
				discount = float64(c) / 100
			}
		}
		prices.Yearly.Discounted = prices.Yearly.Price - discount
	}

	if monthly != nil {
		prices.Monthly.Price = monthly.Price

		var discount float64
		for _, c := range monthly.CouponAmountsOff {
			fd := float64(c) / 100
			if fd < discount || discount == 0 {
				discount = float64(c) / 100
			}
		}
		prices.Monthly.Discounted = prices.Monthly.Price - discount
	}

	return prices
}

type Price struct {
	Price      float64 `json:"price"`
	Discounted float64 `json:"discount"`
}
