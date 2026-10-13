package pricing

import (
	"context"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

// Static uses prices the user provides, so nothing is hardcoded and nothing leaves the machine.
type Static struct {
	OnDemandVCPUHour float64 `json:"onDemandVcpuHour"`
	// SpotDiscount is how much cheaper Spot is than on-demand, from 0 to below 1.
	SpotDiscount float64 `json:"spotDiscount"`
	Currency     string  `json:"currency"`
	source       string
}

// NewStatic checks the prices.
func NewStatic(onDemandVCPUHour, spotDiscount float64, currency string) (Static, error) {
	if err := validRate(onDemandVCPUHour, spotDiscount); err != nil {
		return Static{}, err
	}
	if currency == "" {
		currency = "USD"
	}
	return Static{OnDemandVCPUHour: onDemandVCPUHour, SpotDiscount: spotDiscount, Currency: currency, source: "prices given on the command line"}, nil
}

// StaticFromFile reads a file such as:
//
//	onDemandVcpuHour: 0.0416
//	spotDiscount: 0.65
//	currency: USD
//
// Unknown fields are errors.
func StaticFromFile(path string) (Static, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Static{}, err
	}
	var s Static
	if err := yaml.UnmarshalStrict(data, &s); err != nil {
		return Static{}, fmt.Errorf("%s: %w", path, err)
	}
	st, err := NewStatic(s.OnDemandVCPUHour, s.SpotDiscount, s.Currency)
	if err != nil {
		return Static{}, fmt.Errorf("%s: %w", path, err)
	}
	st.source = "prices from " + path
	return st, nil
}

func (s Static) Rates(context.Context) (Rates, error) {
	return Rates{OnDemandVCPUHour: s.OnDemandVCPUHour, SpotVCPUHour: s.OnDemandVCPUHour * (1 - s.SpotDiscount), Currency: s.Currency,
		Source: s.source, Notes: []string{}}, nil
}
