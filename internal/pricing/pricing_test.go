package pricing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
)

func TestCompute(t *testing.T) {
	// 100 vCPU at $0.04 on-demand and $0.012 on Spot (70% cheaper); 20 move in phase 1, 60 by phase 2.
	r := Rates{OnDemandVCPUHour: 0.04, SpotVCPUHour: 0.012, Currency: "USD"}
	e := Compute(r, 100_000, 20_000, 60_000)
	// 100 x 0.04 x 730 = 2920; phase 1: (80 x 0.04 + 20 x 0.012) x 730 = 2511.2; phase 2: (40 x 0.04 + 60 x 0.012) x 730 = 1693.6.
	if e.CurrentOnDemand != 2920 || e.AfterPhase1 != 2511.2 || e.AfterPhase2 != 1693.6 {
		t.Fatalf("estimate: %+v", e)
	}
	if e.Savings != 1226.4 || e.SavingsPercent != 42 {
		t.Fatalf("savings: %v (%v%%)", e.Savings, e.SavingsPercent)
	}
	if !strings.Contains(e.Disclaimer, "not a billing forecast") || e.HoursPerMonth != 730 {
		t.Fatalf("disclaimer: %+v", e)
	}
	if z := Compute(r, 0, 0, 0); z.SavingsPercent != 0 || z.CurrentOnDemand != 0 {
		t.Fatalf("no cpu: %+v", z)
	}
}

func TestStatic(t *testing.T) {
	s, err := NewStatic(0.04, 0.7, "")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.Rates(context.Background())
	if r.OnDemandVCPUHour != 0.04 || fmt.Sprintf("%.3f", r.SpotVCPUHour) != "0.012" || r.Currency != "USD" || !strings.Contains(r.Source, "command line") {
		t.Fatalf("rates: %+v", r)
	}
	for _, bad := range [][2]float64{{0, 0.5}, {-1, 0.5}, {0.04, 1}, {0.04, -0.1}} {
		if _, err := NewStatic(bad[0], bad[1], ""); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}

	path := filepath.Join(t.TempDir(), "prices.yaml")
	write := func(doc string) {
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("onDemandVcpuHour: 0.05\nspotDiscount: 0.6\ncurrency: EUR\n")
	f, err := StaticFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := f.Rates(context.Background()); r.Currency != "EUR" || r.OnDemandVCPUHour != 0.05 || !strings.Contains(r.Source, path) {
		t.Fatalf("file rates: %+v", r)
	}
	write("onDemandVcpuHour: 0.05\nspotDiscount: 0.6\nspotDiscont: 0.1\n")
	if _, err := StaticFromFile(path); err == nil {
		t.Fatal("an unknown field must be an error")
	}
	write("onDemandVcpuHour: 0.05\nspotDiscount: 2\n")
	if _, err := StaticFromFile(path); err == nil || !strings.Contains(err.Error(), "prices.yaml") {
		t.Fatalf("invalid discount: %v", err)
	}
}

type fakePriceList struct {
	items []string
	err   error
	got   *awspricing.GetProductsInput
}

func (f *fakePriceList) GetProducts(_ context.Context, in *awspricing.GetProductsInput, _ ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error) {
	f.got = in
	return &awspricing.GetProductsOutput{PriceList: f.items}, f.err
}

const m6iLarge = `{"product":{"attributes":{"vcpu":"2","instanceType":"m6i.large"}},"terms":{"OnDemand":{"SKU.TERM":{"priceDimensions":{"SKU.TERM.DIM":{"unit":"Hrs","pricePerUnit":{"USD":"0.0960000000"}}}}}}}`

func advisor(t *testing.T, body string, status int) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAWS(t *testing.T) {
	data := `{"ranges":[{"index":0,"label":"<5%"},{"index":2,"label":"10-15%"}],"spot_advisor":{"us-east-1":{"Linux":{"m6i.large":{"s":70,"r":2}}}}}`
	client := &fakePriceList{items: []string{m6iLarge}}
	r, err := AWS{Region: "us-east-1", Client: client, AdvisorURL: advisor(t, data, 200)}.Rates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// $0.096 per hour for 2 vCPU is $0.048 per vCPU-hour; Spot is 70% cheaper.
	if fmt.Sprintf("%.4f", r.OnDemandVCPUHour) != "0.0480" || fmt.Sprintf("%.4f", r.SpotVCPUHour) != "0.0144" || r.Currency != "USD" {
		t.Fatalf("rates: %+v", r)
	}
	if !strings.Contains(r.Source, "m6i.large") || !strings.Contains(r.Source, "70% cheaper") || !strings.Contains(strings.Join(r.Notes, "\n"), "10-15%") {
		t.Fatalf("source and notes: %q %v", r.Source, r.Notes)
	}
	// The request asks for Linux, shared tenancy, on-demand capacity of the right instance and region.
	var asked []string
	for _, f := range client.got.Filters {
		asked = append(asked, *f.Field+"="+*f.Value)
	}
	for _, want := range []string{"instanceType=m6i.large", "regionCode=us-east-1", "operatingSystem=Linux", "tenancy=Shared"} {
		if !strings.Contains(strings.Join(asked, ","), want) {
			t.Errorf("filters %v are missing %s", asked, want)
		}
	}
}

func TestAWSFailures(t *testing.T) {
	ctx := context.Background()
	// No price: the error says what to do.
	_, err := AWS{Region: "us-east-1", Client: &fakePriceList{}}.Rates(ctx)
	if err == nil || !strings.Contains(err.Error(), "no on-demand price") || !strings.Contains(err.Error(), "--pricing static") {
		t.Fatalf("no price: %v", err)
	}
	_, err = AWS{Region: "us-east-1", Client: &fakePriceList{err: fmt.Errorf("no credentials")}}.Rates(ctx)
	if err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("API error: %v", err)
	}
	if _, err := (AWS{Client: &fakePriceList{items: []string{m6iLarge}}}).Rates(ctx); err == nil || !strings.Contains(err.Error(), "--region") {
		t.Fatalf("no region: %v", err)
	}
	// Without the Advisor, the estimate shows no savings instead of inventing a discount.
	r, err := AWS{Region: "us-east-1", Client: &fakePriceList{items: []string{m6iLarge}}, AdvisorURL: advisor(t, "", 500)}.Rates(ctx)
	if err != nil || r.SpotVCPUHour != r.OnDemandVCPUHour || !strings.Contains(strings.Join(r.Notes, "\n"), "Spot is priced as on-demand") {
		t.Fatalf("advisor down: %+v, %v", r, err)
	}
	r, err = AWS{Region: "eu-west-9", InstanceType: "m6i.large", Client: &fakePriceList{items: []string{m6iLarge}},
		AdvisorURL: advisor(t, `{"spot_advisor":{"us-east-1":{}}}`, 200)}.Rates(ctx)
	if err != nil || r.SpotVCPUHour != r.OnDemandVCPUHour || !strings.Contains(strings.Join(r.Notes, "\n"), "not in the Spot Instance Advisor") {
		t.Fatalf("unknown instance: %+v, %v", r, err)
	}
}
