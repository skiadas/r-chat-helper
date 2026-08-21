package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fakeCatalog = `{
  "opencode-go": {
    "models": {
      "deepseek-v4-flash": {
        "name": "DeepSeek V4 Flash (2x usage)",
        "cost": {"input": 0.07, "output": 0.14, "cache_read": 0.0014}
      }
    }
  }
}`

func TestSyncRatesDerivesListPrices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fakeCatalog))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.DBPath = t.TempDir() + "/test.db"
	cfg.ModelsDevURL = srv.URL
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()

	changed, err := app.SyncRates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}

	r, err := app.RateFor(ctx, "class-go", LockedModelID)
	if err != nil || r == nil {
		t.Fatalf("rate not stored: %v", err)
	}
	// list = 2x usage: 0.07*2=0.14 -> 140000 micros per 1M input.
	if r.InputMicros != 140000 || r.OutputMicros != 280000 || r.CacheMicros != 2800 {
		t.Fatalf("rate = %+v, want input=140000 output=280000 cache=2800", r)
	}

	// Second sync must report no changes.
	changed, err = app.SyncRates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("second sync changed = %d, want 0", changed)
	}
}

func TestUsageMultiplier(t *testing.T) {
	cases := map[string]float64{
		"DeepSeek V4 Flash (2x usage)": 2,
		"GLM 5.2":                       1,
		"foo (3.5x usage)":              3.5,
	}
	for name, want := range cases {
		if got := usageMultiplier(name); got != want {
			t.Errorf("usageMultiplier(%q) = %v, want %v", name, got, want)
		}
	}
}
