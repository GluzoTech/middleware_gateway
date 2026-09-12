package dto_test

import (
	"encoding/json"
	"testing"

	"github.com/gluzo/integration-gateway/app/integrations/easyecom/dto"
)

func TestFlexTypes(t *testing.T) {
	type doc struct {
		S dto.FlexString `json:"s"`
		F dto.FlexFloat  `json:"f"`
		I dto.FlexInt    `json:"i"`
	}
	tests := []struct {
		name    string
		in      string
		want    doc
		wantErr bool
	}{
		{"strings", `{"s":"abc","f":"12.50","i":"7"}`, doc{"abc", 12.5, 7}, false},
		{"numbers", `{"s":12345,"f":12.5,"i":7}`, doc{"12345", 12.5, 7}, false},
		{"integral float as int", `{"i":7.0}`, doc{I: 7}, false},
		{"nulls", `{"s":null,"f":null,"i":null}`, doc{}, false},
		{"empty strings", `{"s":"","f":"","i":" "}`, doc{}, false},
		{"absent", `{}`, doc{}, false},
		{"boolean string", `{"s":true}`, doc{S: "true"}, false},
		{"non-numeric float", `{"f":"abc"}`, doc{}, true},
		{"fractional int", `{"i":"7.5"}`, doc{}, true},
		{"object as string", `{"s":{"x":1}}`, doc{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got doc
			err := json.Unmarshal([]byte(tt.in), &got)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnvelope(t *testing.T) {
	var env dto.Envelope
	if err := json.Unmarshal([]byte(`{"code":200,"message":"ok","data":[{"a":1}]}`), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Code != 200 || env.Message != "ok" || string(env.Data) != `[{"a":1}]` {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}
