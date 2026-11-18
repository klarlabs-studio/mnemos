package domain

import (
	"reflect"
	"testing"
)

func TestUpsertComponents(t *testing.T) {
	stored := map[string]float64{
		CreditAppliedComponentKey: 0.2, CreditComponentPrefix + "d1:c": 0.2,
		SalienceComponentKey: 0.9,
	}
	tests := []struct {
		name     string
		incoming map[string]float64
		want     map[string]float64
	}{
		{"nothing incoming: the stored map stands", nil, stored},
		{"empty incoming: the stored map stands", map[string]float64{}, stored},
		{"incoming credit wins as given", map[string]float64{CreditAppliedComponentKey: -0.1},
			map[string]float64{CreditAppliedComponentKey: -0.1}},
		{"incoming without credit keeps the stored credit", map[string]float64{SalienceComponentKey: 0.4},
			map[string]float64{SalienceComponentKey: 0.4, CreditAppliedComponentKey: 0.2, CreditComponentPrefix + "d1:c": 0.2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UpsertComponents(tt.incoming, stored); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UpsertComponents = %v, want %v", got, tt.want)
			}
		})
	}
	if got := UpsertComponents(map[string]float64{SalienceComponentKey: 0.4}, nil); !reflect.DeepEqual(got, map[string]float64{SalienceComponentKey: 0.4}) {
		t.Errorf("no stored map: got %v, want the incoming map", got)
	}
}
