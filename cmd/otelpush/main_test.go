package main

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

type fakeClock struct {
	Time time.Time
}

func (c fakeClock) Now() time.Time {
	return c.Time
}

func TestMain(m *testing.M) {
	clock = fakeClock{
		Time: time.Now(),
	}

	m.Run()
}
func TestParseMetricLine(t *testing.T) {
	tests := map[string]struct {
		Line string
		Want Metric
	}{
		"int": {
			Line: `metric_name{foo="bar", baz="qux"} 300`,
			Want: Metric{
				Name: "metric_name",
				Gauge: Gauge{
					DataPoints: []DataPoint{
						DataPoint{
							AsDouble:     300,
							TimeUnixNano: clock.Now().UnixNano(),
							Attributes: []Attribute{
								Attribute{
									Key: "foo",
									Value: map[string]string{
										"stringValue": "bar",
									},
								},
								Attribute{
									Key: "baz",
									Value: map[string]string{
										"stringValue": "qux",
									},
								},
							},
						},
					},
				},
			},
		},
		"double": {
			Line: `metric_name{foo="bar", baz="qux"} 300.5`,
			Want: Metric{
				Name: "metric_name",
				Gauge: Gauge{
					DataPoints: []DataPoint{
						DataPoint{
							AsDouble:     300.5,
							TimeUnixNano: clock.Now().UnixNano(),
							Attributes: []Attribute{
								Attribute{
									Key: "foo",
									Value: map[string]string{
										"stringValue": "bar",
									},
								},
								Attribute{
									Key: "baz",
									Value: map[string]string{
										"stringValue": "qux",
									},
								},
							},
						},
					},
				},
			},
		},
		"no label": {
			Line: `metric_name 300.5`,
			Want: Metric{
				Name: "metric_name",
				Gauge: Gauge{
					DataPoints: []DataPoint{
						DataPoint{
							AsDouble:     300.5,
							TimeUnixNano: clock.Now().UnixNano(),
							Attributes:   nil,
						},
					},
				},
			},
		},
		"negative value": {
			Line: `metric_name -300.5`,
			Want: Metric{
				Name: "metric_name",
				Gauge: Gauge{
					DataPoints: []DataPoint{
						DataPoint{
							AsDouble:     -300.5,
							TimeUnixNano: clock.Now().UnixNano(),
							Attributes:   nil,
						},
					},
				},
			},
		},
	}

	for label, tt := range tests {
		t.Run(label, func(t *testing.T) {
			got, err := parseMetricLine(tt.Line)
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(got, tt.Want); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}
