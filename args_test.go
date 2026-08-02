package main

import "testing"

func TestParseArgsConfigPosition(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantConfig string
		wantMode   string
	}{
		{"config before mode equals form", []string{"--config=/tmp/fs-sim.yaml", "populate"}, "/tmp/fs-sim.yaml", "populate"},
		{"config after mode equals form", []string{"populate", "--config=/tmp/fs-sim.yaml"}, "/tmp/fs-sim.yaml", "populate"},
		{"config before mode separate form", []string{"--config", "/tmp/fs-sim.yaml", "update"}, "/tmp/fs-sim.yaml", "update"},
		{"config after mode separate form", []string{"update", "--config", "/tmp/fs-sim.yaml"}, "/tmp/fs-sim.yaml", "update"},
		{"explicit mode flag", []string{"--mode=deep", "--config=/tmp/deep.yaml"}, "/tmp/deep.yaml", "deep"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath, mode, showVersion, err := parseArgs(tt.args)
			if err != nil {
				t.Fatalf("parseArgs returned error: %v", err)
			}
			if showVersion {
				t.Fatal("showVersion = true, want false")
			}
			if configPath != tt.wantConfig {
				t.Errorf("configPath = %q, want %q", configPath, tt.wantConfig)
			}
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
		})
	}
}

func TestParseArgsRejectsMultipleModes(t *testing.T) {
	_, _, _, err := parseArgs([]string{"populate", "update"})
	if err == nil {
		t.Fatal("parseArgs accepted multiple modes")
	}
}
