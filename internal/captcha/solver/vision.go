package solver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/model"
)

// Vision solves image challenges by asking a vision-capable model (via the
// router) to pick the regions to click. It declares NeedsImage so the resolver
// only routes image challenges to it. Mock-tested; needs a real vision model to
// be useful.
type Vision struct {
	Router model.ModelRouter
}

// NewVision builds a Vision solver.
func NewVision(r model.ModelRouter) *Vision { return &Vision{Router: r} }

// Name implements captcha.Solver.
func (v *Vision) Name() string { return "vision" }

// Describe implements captcha.Solver.
func (v *Vision) Describe() captcha.Capabilities {
	return captcha.Capabilities{
		Vendors:    []captcha.Vendor{captcha.VendorCustom, captcha.VendorRecaptchaV2, captcha.VendorHCaptcha},
		Kinds:      []captcha.Kind{captcha.KindImage},
		NeedsImage: true,
		EstCostUSD: 0.01, EstLatencyMS: 3000,
	}
}

type visionPick struct {
	Regions []captcha.RegionClick `json:"regions"`
}

// Solve implements captcha.Solver.
func (v *Vision) Solve(ctx context.Context, c captcha.Challenge) (captcha.Result, error) {
	if v.Router == nil || len(c.Image) == 0 {
		return captcha.Result{}, captcha.ErrDeclined
	}
	prov := v.Router.For(model.TaskVision)
	if prov == nil || !prov.Capabilities().Vision {
		return captcha.Result{}, fmt.Errorf("vision: no vision-capable model configured")
	}
	msg := model.Message{Role: model.User, Images: [][]byte{c.Image},
		Text: `This is an image CAPTCHA grid. Return the pixel regions to click as JSON {"regions":[{"X":..,"Y":..,"W":..,"H":..}]}. Use image pixel coordinates.`}
	raw, _, err := prov.Structured(ctx, []model.Message{msg}, json.RawMessage(`{"type":"object","properties":{"regions":{"type":"array"}},"required":["regions"]}`), model.ChatOpts{})
	if err != nil {
		return captcha.Result{}, err
	}
	var pick visionPick
	if err := json.Unmarshal(raw, &pick); err != nil || len(pick.Regions) == 0 {
		return captcha.Result{}, captcha.ErrDeclined
	}
	return captcha.Result{Kind: captcha.ResultUIActions, Actions: pick.Regions}, nil
}

// Report implements captcha.Solver.
func (v *Vision) Report(context.Context, string, bool) error { return nil }
