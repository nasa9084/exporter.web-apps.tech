package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDataPointJSON(t *testing.T) {
	tests := map[string]struct {
		DataPoint DataPoint
		Want      string
	}{
		"int": {
			DataPoint: &IntDataPoint{
				AsInt: 1,
			},
			Want: `{"asInt":1,"timeUnixNano":0,"attributes":null}
`,
		},
		"double": {
			DataPoint: &DoubleDataPoint{
				AsDouble: 2.4,
			},
			Want: `{"asDouble":2.4,"timeUnixNano":0,"attributes":null}
`,
		},
	}

	for label, tt := range tests {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			if err := json.NewEncoder(&buf).Encode(tt.DataPoint); err != nil {
				t.Fatal(err)
			}

			got := buf.String()
			if diff := cmp.Diff(got, tt.Want); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}
