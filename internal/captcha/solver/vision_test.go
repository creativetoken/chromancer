package solver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/model"
)

// visionProv is a scripted vision-capable provider.
type visionProv struct{ reply string }

func (visionProv) Name() string                     { return "vision-mock" }
func (visionProv) Capabilities() model.Capabilities { return model.Capabilities{Vision: true} }
func (visionProv) Chat(context.Context, []model.Message, model.ChatOpts) (*model.Completion, error) {
	return &model.Completion{}, nil
}
func (p visionProv) Structured(_ context.Context, _ []model.Message, _ json.RawMessage, _ model.ChatOpts) (json.RawMessage, *model.Usage, error) {
	return json.RawMessage(p.reply), &model.Usage{}, nil
}

func router(reply string) model.ModelRouter {
	return &model.StaticRouter{Default: visionProv{reply: reply}}
}

func TestVisionText(t *testing.T) {
	v := NewVision(router(`{"text":"A1B2C3","confidence":0.9}`))
	ch := captcha.Challenge{Vendor: captcha.VendorCustom, Kind: captcha.KindImage, Image: []byte("png"),
		Params: map[string]string{captcha.ParamClass: "text", captcha.ParamSolveID: "s1"}}
	r, err := v.Solve(context.Background(), ch)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if r.Kind != captcha.ResultText || r.Text != "A1B2C3" {
		t.Fatalf("result = %+v", r)
	}
	// Accepted outcome credits the model's accuracy.
	_ = v.Report(context.Background(), "s1", true)
	if acc := v.Accuracy()["vision-mock"]; acc.Attempts != 1 || acc.Accepted != 1 {
		t.Errorf("accuracy = %+v, want 1/1", acc)
	}
}

func TestVisionGrid(t *testing.T) {
	v := NewVision(router(`{"regions":[{"X":10,"Y":20,"W":100,"H":100}],"confidence":0.8}`))
	ch := captcha.Challenge{Vendor: captcha.VendorRecaptchaV2, Kind: captcha.KindImage, Image: []byte("png"),
		Params: map[string]string{captcha.ParamClass: "image_grid"}}
	r, err := v.Solve(context.Background(), ch)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if r.Kind != captcha.ResultUIActions || len(r.Actions) != 1 || r.Actions[0].W != 100 {
		t.Fatalf("result = %+v", r)
	}
}

func TestVisionLowConfidenceDeclines(t *testing.T) {
	v := NewVision(router(`{"text":"maybe","confidence":0.2}`))
	ch := captcha.Challenge{Vendor: captcha.VendorCustom, Kind: captcha.KindImage, Image: []byte("png"),
		Params: map[string]string{captcha.ParamClass: "text"}}
	if _, err := v.Solve(context.Background(), ch); err != captcha.ErrDeclined {
		t.Fatalf("low confidence should decline, got %v", err)
	}
}

func TestVisionNoModelDeclines(t *testing.T) {
	v := NewVision(nil)
	if _, err := v.Solve(context.Background(), captcha.Challenge{Kind: captcha.KindImage, Image: []byte("x")}); err != captcha.ErrDeclined {
		t.Fatalf("no router should decline, got %v", err)
	}
}
