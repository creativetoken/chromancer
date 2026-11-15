package auth

import (
	"testing"
	"time"
)

func TestJWTExpiry(t *testing.T) {
	// {"alg":"HS256"}.{"exp":1700000000}.sig
	tok := "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE3MDAwMDAwMDAsInN1YiI6IjEifQ.abc"
	exp := JWTExpiry("Bearer " + tok)
	if exp == nil || exp.Unix() != 1700000000 {
		t.Fatalf("exp: %v", exp)
	}
	if JWTExpiry("not a jwt", "also-not") != nil {
		t.Fatal("non-jwt should give nil")
	}
	if JWTExpiry() != nil {
		t.Fatal("empty")
	}
	_ = time.Now
}
