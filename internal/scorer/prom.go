package scorer

import (
	"context"
	"fmt"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Querier is the slice of the Prometheus API the brain uses. A fake implements it in tests.
type Querier interface {
	// Query runs an instant query and returns its vector.
	Query(ctx context.Context, promql string) (model.Vector, error)
	// RuleGroups returns the names of the rule groups Prometheus has loaded.
	RuleGroups(ctx context.Context) ([]string, error)
}

// PromClient talks to a Prometheus HTTP API.
type PromClient struct {
	api     promv1.API
	timeout time.Duration
}

// NewPromClient returns a client for the Prometheus at url (e.g. http://prometheus.monitoring.svc:9090).
func NewPromClient(url string) (*PromClient, error) {
	c, err := promapi.NewClient(promapi.Config{Address: url})
	if err != nil {
		return nil, fmt.Errorf("prometheus client for %q: %w", url, err)
	}
	return &PromClient{api: promv1.NewAPI(c), timeout: 15 * time.Second}, nil
}

func (p *PromClient) Query(ctx context.Context, q string) (model.Vector, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	v, _, _, err := p.api.Query(ctx, q, time.Now())
	if err != nil {
		return nil, fmt.Errorf("prometheus query %q: %w", q, err)
	}
	vec, ok := v.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("prometheus query %q returned %s, want a vector", q, v.Type())
	}
	return vec, nil
}

func (p *PromClient) RuleGroups(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	r, err := p.api.Rules(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("prometheus rules API: %w", err)
	}
	names := make([]string, 0, len(r.Groups))
	for _, g := range r.Groups {
		names = append(names, g.Name)
	}
	return names, nil
}
