package verify

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// DecodeOTPAuthQR decodes a QR code image (PNG or JPEG) and returns the
// otpauth:// URI it contains. It is used during 2FA enrolment when the app only
// shows a QR and not a manual-entry key. It errors if the image has no QR code,
// or the QR does not hold an otpauth URI.
func DecodeOTPAuthQR(imgBytes []byte) (string, error) {
	im, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return "", fmt.Errorf("qr: decode image: %w", err)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(im)
	if err != nil {
		return "", fmt.Errorf("qr: bitmap: %w", err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return "", fmt.Errorf("qr: no QR code found: %w", err)
	}
	text := res.GetText()
	if !strings.HasPrefix(strings.ToLower(text), "otpauth://") {
		return "", fmt.Errorf("qr: QR does not hold an otpauth uri")
	}
	return text, nil
}
