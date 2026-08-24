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
			opts, err := parseArgs(tt.args)
			if err != nil {
				t.Fatalf("parseArgs returned error: %v", err)
			}
			if opts.showVersion {
				t.Fatal("showVersion = true, want false")
			}
			if opts.configPath != tt.wantConfig {
				t.Errorf("configPath = %q, want %q", opts.configPath, tt.wantConfig)
			}
			if opts.mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", opts.mode, tt.wantMode)
			}
		})
	}
}

func TestParseArgsRejectsMultipleModes(t *testing.T) {
	_, err := parseArgs([]string{"populate", "update"})
	if err == nil {
		t.Fatal("parseArgs accepted multiple modes")
	}
}

func TestParseArgsResumeAndShard(t *testing.T) {
	opts, err := parseArgs([]string{"populate", "--resume", "--shard=node1"})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}
	if !opts.resume {
		t.Error("resume = false, want true")
	}
	if opts.shard != "node1" {
		t.Errorf("shard = %q, want %q", opts.shard, "node1")
	}

	if _, err := parseArgs([]string{"update", "--resume"}); err == nil {
		t.Error("parseArgs accepted --resume with a non-populate mode")
	}
}
