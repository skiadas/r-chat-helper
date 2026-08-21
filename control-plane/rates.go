package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// ProviderID is the provider label the cost engine uses; the catalog source
// is the opencode-go provider on models.dev.
const providerID = "class-go"

// Models in this list are the only models students may use. The control plane
// fetches their list-price rates from models.dev and stores them for the cost
// engine.
var allowedModels = []allowedModel{
	{modelID: LockedModelID, catalogProvider: "opencode-go"},
}

type allowedModel struct {
	modelID         string // model id (locked for all requests)
	catalogProvider string // models.dev provider to source rates from
}

// models.dev catalog, only the fields we need.
type modelsDevCatalog map[string]struct {
	Models map[string]struct {
		Cost struct {
			Input     float64 `json:"input"`
			Output    float64 `json:"output"`
			CacheRead float64 `json:"cache_read"`
		} `json:"cost"`
		Name string `json:"name"`
	} `json:"models"`
}

var usageMultiplierRe = regexp.MustCompile(`(\d+(?:\.\d+)?)x usage`)

func usageMultiplier(name string) float64 {
	m := usageMultiplierRe.FindStringSubmatch(name)
	if len(m) == 2 {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil && f > 0 {
			return f
		}
	}
	return 1
}

// SyncRates fetches the models.dev catalog and upserts list-price rates for
// the allowed models. Returns the number of models whose rates changed.
func (a *App) SyncRates(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.ModelsDevURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("models.dev returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return 0, err
	}
	var cat modelsDevCatalog
	if err := json.Unmarshal(body, &cat); err != nil {
		return 0, fmt.Errorf("parse models.dev: %w", err)
	}

	now := time.Now().Unix()
	changed := 0
	for _, am := range allowedModels {
		prov, ok := cat[am.catalogProvider]
		if !ok {
			return changed, fmt.Errorf("provider %q missing from catalog", am.catalogProvider)
		}
		model, ok := prov.Models[am.modelID]
		if !ok {
			return changed, fmt.Errorf("model %q missing from %q", am.modelID, am.catalogProvider)
		}
		m := usageMultiplier(model.Name)
		r := Rate{
			Provider:     providerID,
			Model:        am.modelID,
			Multiplier:   m,
			InputMicros:  int64(math.Round(model.Cost.Input * m * 1e6)),
			OutputMicros: int64(math.Round(model.Cost.Output * m * 1e6)),
			CacheMicros:  int64(math.Round(model.Cost.CacheRead * m * 1e6)),
			FetchedAt:    now,
		}
		prev, err := a.RateFor(ctx, r.Provider, r.Model)
		if err != nil {
			return changed, err
		}
		if prev != nil && prev.InputMicros == r.InputMicros && prev.OutputMicros == r.OutputMicros && prev.CacheMicros == r.CacheMicros {
			continue
		}
		if err := a.UpsertRate(ctx, r); err != nil {
			return changed, err
		}
		if prev == nil {
			log.Printf("rates: seeded %s/%s at list $%.4f/%.4f/%.4f per 1M", r.Provider, r.Model,
				float64(r.InputMicros)/1e6, float64(r.OutputMicros)/1e6, float64(r.CacheMicros)/1e6)
		} else {
			log.Printf("rates: updated %s/%s (input %.4f -> %.4f, output %.4f -> %.4f)",
				r.Provider, r.Model, float64(prev.InputMicros)/1e6, float64(r.InputMicros)/1e6,
				float64(prev.OutputMicros)/1e6, float64(r.OutputMicros)/1e6)
		}
		changed++
	}
	return changed, nil
}
