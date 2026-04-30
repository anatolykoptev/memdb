package search

import (
	"math"
	"testing"
)

func TestDistributionEntropy_Empty(t *testing.T) {
	if got := distributionEntropy(nil); got != 0 {
		t.Errorf("nil dist: want 0, got %v", got)
	}
	if got := distributionEntropy([]CategoryConfidence{}); got != 0 {
		t.Errorf("empty dist: want 0, got %v", got)
	}
	single := []CategoryConfidence{{Category: QueryCategoryOpenDomain, Confidence: 1}}
	if got := distributionEntropy(single); got != 0 {
		t.Errorf("single-entry dist: want 0, got %v", got)
	}
}

func TestDistributionEntropy_Uniform(t *testing.T) {
	// 5-way uniform → entropy = ln(5) ≈ 1.6094.
	uniform := make([]CategoryConfidence, 5)
	for i := range uniform {
		uniform[i] = CategoryConfidence{Confidence: 0.2}
	}
	got := distributionEntropy(uniform)
	want := math.Log(5)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("uniform 5-way: want %v, got %v", want, got)
	}
}

func TestDistributionEntropy_OneHot(t *testing.T) {
	// One-hot → entropy = 0 (only one non-zero entry).
	oneHot := []CategoryConfidence{
		{Confidence: 1.0},
		{Confidence: 0.0},
		{Confidence: 0.0},
		{Confidence: 0.0},
		{Confidence: 0.0},
	}
	if got := distributionEntropy(oneHot); got != 0 {
		t.Errorf("one-hot: want 0, got %v", got)
	}
}

func TestDistributionEntropy_OrderingInvariant(t *testing.T) {
	// Same probabilities in different order should give the same entropy.
	a := []CategoryConfidence{
		{Confidence: 0.5},
		{Confidence: 0.3},
		{Confidence: 0.2},
	}
	b := []CategoryConfidence{
		{Confidence: 0.2},
		{Confidence: 0.5},
		{Confidence: 0.3},
	}
	if math.Abs(distributionEntropy(a)-distributionEntropy(b)) > 1e-9 {
		t.Errorf("entropy is not order-invariant")
	}
}

func TestRoutingTrace_Top1Label(t *testing.T) {
	cases := []struct {
		name string
		in   d10RoutingTrace
		want string
	}{
		{
			name: "no signal → none sentinel",
			in:   d10RoutingTrace{HasSignal: false, Top1Cat: QueryCategoryTemporal},
			want: d10Top1NoneSentinel,
		},
		{
			name: "has signal → category string",
			in:   d10RoutingTrace{HasSignal: true, Top1Cat: QueryCategorySingleHop},
			want: string(QueryCategorySingleHop),
		},
		{
			name: "has signal but top1 is open_domain",
			in:   d10RoutingTrace{HasSignal: true, Top1Cat: QueryCategoryOpenDomain},
			want: string(QueryCategoryOpenDomain),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.top1Label(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
