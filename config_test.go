package optiimage

import (
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"
)

// A whole configuration, the way it is actually written.
func TestConfig_FetchTimeoutFromAFile(t *testing.T) {
	var opts Options
	if err := json.Unmarshal([]byte(`{"fetchTimeout":"10s","quality":82}`), &opts); err != nil {
		t.Fatalf("Unmarshal = %v, want nil", err)
	}
	if time.Duration(opts.FetchTimeout) != 10*time.Second {
		t.Errorf("FetchTimeout = %v, want 10s", time.Duration(opts.FetchTimeout))
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
		var cfg Options
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

// A Files prefix is a path: it begins and ends with "/".
func TestConfig_RefusesAFilesPrefixThatIsNoPath(t *testing.T) {
	for _, prefix := range []string{"static/", "/static", "//static/"} {
		cfg := Options{Files: map[string]fs.FS{prefix: fstest.MapFS{}}}.withDefaults()
		if err := cfg.validate(); err == nil {
			t.Errorf("prefix %q was accepted", prefix)
		}
	}
	if err := (Options{Files: map[string]fs.FS{"/static/": fstest.MapFS{}}}).withDefaults().validate(); err != nil {
		t.Errorf("/static/ was refused: %v", err)
	}
}
