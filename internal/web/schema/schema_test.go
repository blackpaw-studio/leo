package schema

import (
	"reflect"
	"testing"
)

func TestDeriveKind(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want Kind
	}{
		{"string", reflect.TypeOf(""), KindText},
		{"int", reflect.TypeOf(0), KindNumber},
		{"bool", reflect.TypeOf(false), KindBool},
		{"ptr bool", reflect.TypeOf((*bool)(nil)), KindTriBool},
		{"string slice", reflect.TypeOf([]string{}), KindCSV},
		{"string map", reflect.TypeOf(map[string]string{}), KindEnvMap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveKind(tt.typ); got != tt.want {
				t.Errorf("DeriveKind(%v) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}
