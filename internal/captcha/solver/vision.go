package solver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/creativetoken/chromancer/internal/captcha"
	"github.com/creativetoken/chromancer/internal/model"
)

// Vision solves image challenges with a vision-capable model (via the router):
// a text CAPTCHA returns the read characters (ResultText); an image grid returns
// the tile regions to click (ResultUIActions), with the resolver re-detecting a
// fresh grid each round. It declares NeedsImage so only image challenges route
// here, applies a confidence threshold, and tracks per-model accuracy.
type Vision struct {
	Router  model.ModelRouter
	MinConf float64 // decline below this confidence (default 0.5)
	mu      sync.Mutex
	stats   map[string]*VisionStat // model name -> accuracy
	pending map[string]string      // solveID -> model name (for Report accounting)
}

// VisionStat is per-model vision accuracy.
type VisionStat struct {
	Attempts int `json:"attempts"`
	Accepted int `json:"accepted"`
}

// NewVision builds a Vision solver.
func NewVision(r model.ModelRouter) *Vision {
	return &Vision{Router: r, MinConf: 0.5, stats: map[string]*VisionStat{}, pending: map[string]string{}}
}

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

type visionGrid struct {
	Regions    []captcha.RegionClick `json:"regions"`
	Confidence float64               `json:"confidence"`
}

type visionText struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
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
	id := c.Params[captcha.ParamSolveID]
	min := v.MinConf
	if min == 0 {
		min = 0.5
	}

	if c.Params[captcha.ParamClass] == "text" || c.Kind == captcha.KindCustom {
		msg := model.Message{Role: model.User, Images: [][]byte{c.Image},
			Text: `This is a text CAPTCHA image. Return the exact characters shown, case-sensitive, as JSON {"text":"...","confidence":0..1}.`}
		raw, _, err := prov.Structured(ctx, []model.Message{msg},
			json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"confidence":{"type":"number"}},"required":["text"]}`), model.ChatOpts{})
		if err != nil {
			return captcha.Result{}, err
		}
		var out visionText
		if err := json.Unmarshal(raw, &out); err != nil || out.Text == "" || out.Confidence < min {
			return captcha.Result{}, captcha.ErrDeclined
		}
		v.track(id, prov.Name())
		return captcha.Result{Kind: captcha.ResultText, Text: out.Text, SolveID: id}, nil
	}

	// Image grid: pick the tiles to click (pixel regions).
	msg := model.Message{Role: model.User, Images: [][]byte{c.Image},
		Text: `This is an image CAPTCHA grid. Return the pixel regions to click as JSON {"regions":[{"X":..,"Y":..,"W":..,"H":..}],"confidence":0..1}. Use image pixel coordinates.`}
	raw, _, err := prov.Structured(ctx, []model.Message{msg},
		json.RawMessage(`{"type":"object","properties":{"regions":{"type":"array"},"confidence":{"type":"number"}},"required":["regions"]}`), model.ChatOpts{})
	if err != nil {
		return captcha.Result{}, err
	}
	var pick visionGrid
	if err := json.Unmarshal(raw, &pick); err != nil || len(pick.Regions) == 0 || pick.Confidence < min {
		return captcha.Result{}, captcha.ErrDeclined
	}
	v.track(id, prov.Name())
	return captcha.Result{Kind: captcha.ResultUIActions, Actions: pick.Regions, SolveID: id}, nil
}

func (v *Vision) track(solveID, modelName string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stats[modelName] == nil {
		v.stats[modelName] = &VisionStat{}
	}
	v.stats[modelName].Attempts++
	if solveID != "" {
		v.pending[solveID] = modelName
	}
}

// Report implements captcha.Solver, crediting the model that solved it.
func (v *Vision) Report(_ context.Context, solveID string, accepted bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if m, ok := v.pending[solveID]; ok {
		if accepted && v.stats[m] != nil {
			v.stats[m].Accepted++
		}
		delete(v.pending, solveID)
	}
	return nil
}

// Accuracy returns a copy of per-model vision accuracy.
func (v *Vision) Accuracy() map[string]VisionStat {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]VisionStat{}
	for m, s := range v.stats {
		out[m] = *s
	}
	return out
}
