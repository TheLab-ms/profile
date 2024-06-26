package datamodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewPrices(t *testing.T) {
	items := []*PriceDetails{
		{
			Price:            123,
			CouponAmountsOff: map[string]int64{"foo": 1000, "bar": 300},
		},
		{
			Annual:           true,
			Price:            234,
			CouponAmountsOff: map[string]int64{"foo": 1100, "bar": 350},
		},
		{
			Annual: true,
			Price:  1230, // this won't be picked because it isn't first
		},
		{
			Price: 2340, // this won't be picked because it isn't first
		},
	}

	actual := NewPrices(items)
	exp := &Prices{
		Yearly: Price{
			Price:      234,
			Discounted: 230.5,
		},
		Monthly: Price{
			Price:      123,
			Discounted: 120,
		},
	}
	assert.Equal(t, exp, actual)

	// also doesn't panic when empty
	NewPrices(nil)
}
