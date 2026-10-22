package app

import (
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"testing"

	"github.com/576469377/Agent2API/internal/config"
)

type configurableFixture struct {
	*fakeAdapter
	sanitize bool
}

func (f *configurableFixture) SetSanitize(value bool) { f.sanitize = value }
func (f *configurableFixture) Sanitize() bool         { return f.sanitize }
func (f *configurableFixture) InvalidateModels()      {}

func TestConfigPublishesActualAdapterCapabilitiesAndRuntimeValues(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	hub, err := NewHub([]*PlatformRuntime{
		{ID: "custom-configurable", Adapter: &configurableFixture{fakeAdapter: &fakeAdapter{}, sanitize: true}},
		{ID: "workbuddy", Adapter: &fakeAdapter{}},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	a := New(config.Default(), hub, logger)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/config", nil))
	var response configResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Platforms) != 2 || !response.Platforms[0].SupportsSanitize || !response.Platforms[0].Sanitize || response.Platforms[1].SupportsSanitize {
		t.Fatalf("capability inferred from platform name instead of adapter: %+v", response.Platforms)
	}
}
