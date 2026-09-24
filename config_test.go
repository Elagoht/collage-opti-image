package optiimage

import (
	"encoding/json"
	"testing"
	"time"
)

// A plugin configuration file is written by a person, and a person writes "10s".
// encoding/json unmarshals a time.Duration from nanoseconds and from nothing else,
// so the only spelling anybody was going to use was the one that failed.
func TestDuration_ReadsWhatPeopleWrite(t *testing.T) {
	for _, tc := range []struct {
		json string
		want time.Duration
	}{
		{`"10s"`, 10 * time.Second},
		{`"500ms"`, 500 * time.Millisecond},
		{`"1m30s"`, 90 * time.Second},
		{`10000000000`, 10 * time.Second},
		{`0`, 0},
	} {
		var d Duration
		if err := json.Unmarshal([]byte(tc.json), &d); err != nil {
			t.Errorf("Unmarshal(%s) = %v, want nil", tc.json, err)
			continue
		}
		if time.Duration(d) != tc.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tc.json, time.Duration(d), tc.want)
		}
	}
}

func TestDuration_RefusesNonsense(t *testing.T) {
	for _, in := range []string{`"ten seconds"`, `"10x"`, `true`, `{}`} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("Unmarshal(%s) = nil error, want a refusal", in)
		}
	}
}

// A whole configuration, the way it is actually written.
func TestConfig_FetchTimeoutFromAFile(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"fetchTimeout":"10s","quality":82}`), &cfg); err != nil {
		t.Fatalf("Unmarshal = %v, want nil", err)
	}
	if time.Duration(cfg.FetchTimeout) != 10*time.Second {
		t.Errorf("FetchTimeout = %v, want 10s", time.Duration(cfg.FetchTimeout))
	}
}

// It round-trips into something still worth editing.
func TestDuration_MarshalsReadably(t *testing.T) {
	out, err := json.Marshal(Duration(90 * time.Second))
	if err != nil {
		t.Fatalf("Marshal = %v, want nil", err)
	}
	if string(out) != `"1m30s"` {
		t.Errorf("Marshal = %s, want %q", out, "1m30s")
	}
}

// "webp" was a boolean, and a configuration written for it reads the same. "auto"
// is the third value.
func TestWebPMode_ReadsTheBooleansAndAuto(t *testing.T) {
	for _, tc := range []struct {
		json string
		want WebPMode
	}{
		{`{"webp":false}`, WebPOff},
		{`{"webp":true}`, WebPOn},
		{`{"webp":"auto"}`, WebPAuto},
		{`{}`, WebPOff},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.json), &cfg); err != nil {
			t.Errorf("Unmarshal(%s) = %v, want nil", tc.json, err)
			continue
		}
		if cfg.WebP != tc.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tc.json, cfg.WebP, tc.want)
		}
		out, err := json.Marshal(cfg.WebP)
		if err != nil {
			t.Fatalf("Marshal = %v", err)
		}
		var back WebPMode
		if err := json.Unmarshal(out, &back); err != nil || back != tc.want {
			t.Errorf("round trip of %v through %s = %v, %v", tc.want, out, back, err)
		}
	}
}

func TestWebPMode_RefusesNonsense(t *testing.T) {
	for _, in := range []string{`"yes"`, `"Auto"`, `1`, `{}`} {
		var m WebPMode
		if err := json.Unmarshal([]byte(in), &m); err == nil {
			t.Errorf("Unmarshal(%s) = nil error, want a refusal", in)
		}
	}
}
