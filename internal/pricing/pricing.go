// Package pricing estimates what the migration plan would save. It never has prices of its own:
// the rates come from a provider, either numbers the user gives (static) or the AWS price list (aws).
// The result is an estimate from requested CPU, not a billing forecast.
package pricing

import (
	"context"
	"fmt"
	"math"
)

// HoursPerMonth is the usual 730-hour month of cloud price lists.
const HoursPerMonth = 730

// Rates are the prices of one vCPU for one hour.
type Rates struct {
	OnDemandVCPUHour float64 `json:"onDemandVcpuHour"`
	SpotVCPUHour     float64 `json:"spotVcpuHour"`
	// Currency is "USD" for the AWS provider; the static provider takes it from the file or flag.
	Currency string `json:"currency"`
	// Source says where the rates come from, and Notes what to keep in mind about them.
	Source string   `json:"source"`
	Notes  []string `json:"notes"`
}

// Provider gives the rates.
type Provider interface {
	Rates(ctx context.Context) (Rates, error)
}

// Estimate is the monthly compute cost at the rates, for the plan's three states.
type Estimate struct {
	Rates           Rates   `json:"rates"`
	HoursPerMonth   int     `json:"hoursPerMonth"`
	CurrentOnDemand float64 `json:"currentOnDemand"`
	AfterPhase1     float64 `json:"afterPhase1"`
	AfterPhase2     float64 `json:"afterPhase2"`
	// Savings is the monthly difference between now and after phase 2, and SavingsPercent its share.
	Savings        float64 `json:"estimatedSavings"`
	SavingsPercent float64 `json:"estimatedSavingsPercent"`
	// Disclaimer repeats that this is not a bill.
	Disclaimer string `json:"disclaimer"`
}

// Compute estimates the cost from the requested CPU, the CPU that moves in phase 1 and the CPU that
// moves by the end of phase 2, all in millicores. Everything that does not move stays on-demand.
func Compute(r Rates, totalMilli, phase1Milli, phase2Milli int64) Estimate {
	cost := func(spotMilli int64) float64 {
		onDemand := float64(totalMilli-spotMilli) / 1000
		spot := float64(spotMilli) / 1000
		return (onDemand*r.OnDemandVCPUHour + spot*r.SpotVCPUHour) * HoursPerMonth
	}
	e := Estimate{Rates: r, HoursPerMonth: HoursPerMonth, CurrentOnDemand: cost(0), AfterPhase1: cost(phase1Milli),
		AfterPhase2: cost(phase2Milli), Disclaimer: "an estimate from requested CPU only: memory, storage, network, discounts and the real instance mix are not included; not a billing forecast"}
	e.Savings = e.CurrentOnDemand - e.AfterPhase2
	if e.CurrentOnDemand > 0 {
		e.SavingsPercent = math.Round(e.Savings/e.CurrentOnDemand*1000) / 10
	}
	for _, v := range []*float64{&e.CurrentOnDemand, &e.AfterPhase1, &e.AfterPhase2, &e.Savings} {
		*v = math.Round(*v*100) / 100
	}
	return e
}

func validRate(onDemand, discount float64) error {
	if onDemand <= 0 {
		return fmt.Errorf("the on-demand price per vCPU-hour must be above 0, got %v", onDemand)
	}
	if discount < 0 || discount >= 1 {
		return fmt.Errorf("the Spot discount must be from 0 to below 1 (0.65 is 65%% cheaper than on-demand), got %v", discount)
	}
	return nil
}
