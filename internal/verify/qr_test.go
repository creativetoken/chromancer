package verify

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// makeQR encodes text into a PNG QR code (test fixture).
func makeQR(t *testing.T, text string) []byte {
	t.Helper()
	bm, err := qrcode.NewQRCodeWriter().Encode(text, gozxing.BarcodeFormat_QR_CODE, 240, 240, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, bm); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeOTPAuthQR(t *testing.T) {
	uri := "otpauth://totp/Acme:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Acme&digits=6&period=30"
	got, err := DecodeOTPAuthQR(makeQR(t, uri))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != uri {
		t.Fatalf("decoded %q, want %q", got, uri)
	}
	// The decoded URI parses into a usable seed.
	sd, err := ParseSeed(got)
	if err != nil || sd.Secret != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("parse decoded: %+v %v", sd, err)
	}
}

func TestDecodeQRNonOTP(t *testing.T) {
	if _, err := DecodeOTPAuthQR(makeQR(t, "https://example.com/not-otp")); err == nil {
		t.Fatal("a non-otpauth QR should be rejected")
	}
}
