package main

import (
	"os"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/manifest"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"gopkg.in/yaml.v3"
)

// TestManifestParses runs the host's own manifest parser over module.yaml, so
// a manifest the host would refuse fails the build instead of the review.
func TestManifestParses(t *testing.T) {
	raw, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse("module.yaml", raw, yaml.Unmarshal)
	if err != nil {
		t.Fatalf("module.yaml: %v", err)
	}
	if m.Name != name {
		t.Errorf("manifest name %q, binary name %q", m.Name, name)
	}
	if m.Version != string(release) {
		t.Errorf("manifest version %q, binary version %q", m.Version, release)
	}
	if m.ContractMajor != 1 {
		t.Errorf("contract_major %d, want 1", m.ContractMajor)
	}
	if m.License == "" {
		t.Error("no license")
	}
	if len(m.Options) == 0 {
		t.Error("no options")
	}
	for _, opt := range m.Options {
		t.Logf("option %s (%s) default %q", opt.Key, opt.Type, opt.Default)
	}
	// Every egress entry naming an option must name a declared string/url option.
	declared := map[string]bool{}
	for _, opt := range m.Options {
		declared[opt.Key] = true
	}
	for _, e := range m.Egress {
		if e.Option != "" && !declared[e.Option] {
			t.Errorf("egress option %q is not a declared option", e.Option)
		}
		t.Logf("egress: %s", e.Line())
	}
	if m.UI == nil || len(m.UI.Contributes.Sections[manifest.SlotDeviceTab]) != 1 {
		t.Errorf("ui contributes %+v", m.UI)
	} else {
		t.Logf("device_tab: %+v", m.UI.Contributes.Sections[manifest.SlotDeviceTab][0])
	}
	// Pin the pages entry to the host.register.page('guide', ...) call in
	// ui/src/index.ts: a dropped pages block would parse fine but the host
	// would drop the module whole at runtime for the undeclared registration.
	if len(m.UI.Contributes.Pages) != 1 || m.UI.Contributes.Pages[0].Path != "guide" {
		t.Errorf("ui contributes pages %+v, want exactly [{Path: guide}]", m.UI.Contributes.Pages)
	} else {
		t.Logf("page: %+v", m.UI.Contributes.Pages[0])
	}
}

// TestOpenAPIParses runs the host's fragment parser over openapi.json.
func TestOpenAPIParses(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routesapi.ParseFragment(raw); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
}

// TestManifestSummaryBound: the marketplace refuses module.yaml summaries over
// 500 characters ("Too big: expected string to have <=500 characters").
func TestManifestSummaryBound(t *testing.T) {
	raw, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse("module.yaml", raw, yaml.Unmarshal)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Summary) > 500 {
		t.Errorf("summary is %d characters, the marketplace allows at most 500", len(m.Summary))
	}
}
