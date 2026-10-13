package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

// DefaultInstanceType is the reference instance whose price per vCPU stands for the whole cluster.
const DefaultInstanceType = "m6i.large"

const spotAdvisorURL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"

// priceListAPI is the part of the AWS Price List client that is used.
type priceListAPI interface {
	GetProducts(ctx context.Context, in *awspricing.GetProductsInput, opts ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error)
}

// AWS reads the on-demand price of a reference instance type from the AWS Price List API (it
// needs AWS credentials, as the API is signed, and sends the request to AWS) and the Spot
// savings of that instance from the public Spot Instance Advisor. The Spot price moves all the time:
// the savings are the Advisor's current figure for the region, not a quote.
type AWS struct {
	Region       string
	InstanceType string

	// The fields below are for tests.
	Client     priceListAPI
	HTTP       *http.Client
	AdvisorURL string
}

func (a AWS) Rates(ctx context.Context) (Rates, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	instance := a.InstanceType
	if instance == "" {
		instance = DefaultInstanceType
	}
	client := a.Client
	region := a.Region
	if client == nil {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return Rates{}, fmt.Errorf("load the AWS configuration: %w", err)
		}
		if region == "" {
			region = cfg.Region
		}
		// The Price List API is served from a few regions, whatever region the prices are for.
		client = awspricing.NewFromConfig(cfg, func(o *awspricing.Options) { o.Region = "us-east-1" })
	}
	if region == "" {
		return Rates{}, fmt.Errorf("no AWS region: give --region (for example us-east-1) or configure one")
	}
	perVCPU, err := onDemandPerVCPU(ctx, client, region, instance)
	if err != nil {
		return Rates{}, fmt.Errorf("AWS Price List for %s in %s: %w (the API needs AWS credentials; --pricing static takes prices you provide instead)", instance, region, err)
	}
	r := Rates{OnDemandVCPUHour: perVCPU, SpotVCPUHour: perVCPU, Currency: "USD",
		Source: fmt.Sprintf("AWS Price List, %s Linux on-demand in %s", instance, region), Notes: []string{}}

	savings, interruption, err := a.spotSavings(ctx, region, instance)
	if err != nil {
		r.Notes = append(r.Notes, fmt.Sprintf("Spot savings could not be read (%v): Spot is priced as on-demand, so the estimate shows no savings", err))
		return r, nil
	}
	r.SpotVCPUHour = perVCPU * (1 - savings)
	r.Source += fmt.Sprintf("; Spot %.0f%% cheaper (AWS Spot Instance Advisor)", savings*100)
	if interruption != "" {
		r.Notes = append(r.Notes, fmt.Sprintf("the Spot Instance Advisor shows an interruption frequency of %s for %s", interruption, instance))
	}
	r.Notes = append(r.Notes, "the rate of one instance type stands for the whole cluster; your real mix of instance types will differ")
	return r, nil
}

// onDemandPerVCPU asks for the Linux, shared tenancy, on-demand price of the instance type.
func onDemandPerVCPU(ctx context.Context, client priceListAPI, region, instance string) (float64, error) {
	filter := func(field, value string) types.Filter {
		return types.Filter{Type: types.FilterTypeTermMatch, Field: aws.String(field), Value: aws.String(value)}
	}
	out, err := client.GetProducts(ctx, &awspricing.GetProductsInput{
		ServiceCode: aws.String("AmazonEC2"),
		Filters: []types.Filter{filter("instanceType", instance), filter("regionCode", region), filter("operatingSystem", "Linux"),
			filter("tenancy", "Shared"), filter("preInstalledSw", "NA"), filter("capacitystatus", "Used")},
		MaxResults: aws.Int32(5),
	})
	if err != nil {
		return 0, err
	}
	for _, item := range out.PriceList {
		var p struct {
			Product struct {
				Attributes struct {
					VCPU string `json:"vcpu"`
				} `json:"attributes"`
			} `json:"product"`
			Terms struct {
				OnDemand map[string]struct {
					PriceDimensions map[string]struct {
						Unit         string            `json:"unit"`
						PricePerUnit map[string]string `json:"pricePerUnit"`
					} `json:"priceDimensions"`
				} `json:"OnDemand"`
			} `json:"terms"`
		}
		if json.Unmarshal([]byte(item), &p) != nil {
			continue
		}
		vcpu, _ := strconv.ParseFloat(p.Product.Attributes.VCPU, 64)
		for _, term := range p.Terms.OnDemand {
			for _, d := range term.PriceDimensions {
				price, _ := strconv.ParseFloat(d.PricePerUnit["USD"], 64)
				if d.Unit == "Hrs" && price > 0 && vcpu > 0 {
					return price / vcpu, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("no on-demand price found (check the region and the instance type)")
}

// spotSavings reads the savings (0 to 1) and the interruption frequency label of the instance type
// from the Spot Instance Advisor data.
func (a AWS) spotSavings(ctx context.Context, region, instance string) (float64, string, error) {
	url := a.AdvisorURL
	if url == "" {
		url = spotAdvisorURL
	}
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("the Spot Instance Advisor answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, "", err
	}
	var data struct {
		Ranges []struct {
			Index int    `json:"index"`
			Label string `json:"label"`
		} `json:"ranges"`
		Advisor map[string]map[string]map[string]struct {
			Savings      int `json:"s"`
			Interruption int `json:"r"`
		} `json:"spot_advisor"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, "", fmt.Errorf("read the Spot Instance Advisor data: %w", err)
	}
	e, ok := data.Advisor[region]["Linux"][instance]
	if !ok {
		return 0, "", fmt.Errorf("%s is not in the Spot Instance Advisor data for %s", instance, region)
	}
	label := ""
	for _, r := range data.Ranges {
		if r.Index == e.Interruption {
			label = r.Label
		}
	}
	return float64(e.Savings) / 100, label, nil
}
