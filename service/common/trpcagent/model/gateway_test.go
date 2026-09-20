package model

import "testing"

func TestNewGatewayValidation(t *testing.T) {
	if _, err := NewGateway(Options{}); err == nil {
		t.Fatal("expected empty model error")
	}
	if _, err := NewGateway(Options{Name: "m"}); err == nil {
		t.Fatal("expected base URL error")
	}
}
