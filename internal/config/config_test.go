package config

import (
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	defaults := Default()
	if err := defaults.Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	if defaults.Test.Connections != 1 {
		t.Fatalf("default connections = %d, want 1", defaults.Test.Connections)
	}
	if defaults.Test.Timeout <= defaults.Test.Duration {
		t.Fatal("default timeout must exceed the default duration")
	}
}

func TestValidateRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero duration", func(c *Config) { c.Test.Duration = 0 }},
		{"timeout not longer than duration", func(c *Config) { c.Test.Timeout = c.Test.Duration }},
		{"zero connections", func(c *Config) { c.Test.Connections = 0 }},
		{"zero samples", func(c *Config) { c.Test.LatencySamples = 0 }},
		{"negative interval", func(c *Config) { c.Test.LatencyInterval = -time.Second }},
		{"negative max bytes", func(c *Config) { c.Test.MaxBytes = -1 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := Default()
			testCase.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}
