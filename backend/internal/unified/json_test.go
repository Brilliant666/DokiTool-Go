package unified

import (
	"encoding/json"
	"testing"
)

func TestLosslessWireValues(t *testing.T) {
	input := []byte(`{"id":"92233720368547758081234567890","large":92233720368547758081234567890,"null":null,"false":false,"zero":0,"list":[]}`)
	value, err := Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	if value["large"].(json.Number).String() != "92233720368547758081234567890" {
		t.Fatal("integer precision lost")
	}
	if _, exists := value["missing"]; exists {
		t.Fatal("invented absent value")
	}
	roundTrip, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(roundTrip)
	if err != nil || !Equal(value, decoded) {
		t.Fatalf("wire round trip failed: %s %v", roundTrip, err)
	}
	if Equal(nil, false) || Equal(false, 0) || Equal(Object{}, Object{"x": nil}) || Equal(Array{}, nil) {
		t.Fatal("different wire states collapsed")
	}
	if !Equal(json.Number("30.0"), 30) || Equal(json.Number("9007199254740993"), json.Number("9007199254740992")) {
		t.Fatal("numeric comparison lost precision")
	}
	for _, invalid := range []string{`{} {}`, `[]`, `null`} {
		if _, err := Decode([]byte(invalid)); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestPHPNumericStringGrammar(t *testing.T) {
	for _, valid := range []string{" 30 ", "+.5", "-1.", "1e3", "\t0\r\n"} {
		if _, ok := Numeric(valid); !ok {
			t.Fatalf("rejected numeric PHP string %q", valid)
		}
	}
	for _, invalid := range []string{"1_000", "0x10", "NaN", "Inf", "", "\u00a030", "1e9999"} {
		if _, ok := Numeric(invalid); ok {
			t.Fatalf("accepted %q", invalid)
		}
	}
}
