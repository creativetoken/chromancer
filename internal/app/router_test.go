package app

import (
	"testing"

	"github.com/creativetoken/chromancer/internal/config"
	"github.com/creativetoken/chromancer/internal/model"
)

// Regression: the vision CAPTCHA solver only engages when the router's model
// reports Vision. buildRouter previously constructed providers without the
// Vision flag, so reCAPTCHA image challenges always fell through to human
// handoff. The config's Vision flag must reach the provider's capabilities.
func TestBuildRouterPropagatesVision(t *testing.T) {
	for _, prov := range []string{"anthropic", "openai", "gemini"} {
		t.Run(prov, func(t *testing.T) {
			r, err := buildRouter(config.LLMConfig{Provider: prov, Model: "m", Vision: true})
			if err != nil {
				t.Fatalf("buildRouter(%s): %v", prov, err)
			}
			p := r.For(model.TaskVision)
			if p == nil {
				t.Fatal("no provider for vision task")
			}
			if !p.Capabilities().Vision {
				t.Errorf("%s provider should report Vision when configured", prov)
			}
		})
	}
	// Vision off must stay off.
	r, err := buildRouter(config.LLMConfig{Provider: "anthropic", Model: "m", Vision: false})
	if err != nil {
		t.Fatal(err)
	}
	if r.For(model.TaskVision).Capabilities().Vision {
		t.Error("Vision:false must not report a vision-capable provider")
	}
}
