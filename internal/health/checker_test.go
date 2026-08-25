package health

import (
	"testing"
	"time"
)

func TestChangedDetectsWhatIsWorthAnnouncing(t *testing.T) {
	base := Report{Score: 85, Findings: []Finding{{Code: CodeBelowFold}}}

	cases := []struct {
		name     string
		previous *Report
		current  Report
		want     bool
	}{
		{
			name:     "first ever check",
			previous: nil,
			current:  base,
			want:     true,
		},
		{
			name:     "nothing moved",
			previous: &base,
			current:  base,
			want:     false,
		},
		{
			name:     "score changed",
			previous: &base,
			current:  Report{Score: 70, Findings: []Finding{{Code: CodeBelowFold}}},
			want:     true,
		},
		{
			name:     "a finding appeared",
			previous: &base,
			current: Report{Score: 85, Findings: []Finding{
				{Code: CodeBelowFold}, {Code: CodeRedirected},
			}},
			want: true,
		},
		{
			name:     "same count, different problem",
			previous: &base,
			current:  Report{Score: 85, Findings: []Finding{{Code: CodeHidden}}},
			want:     true,
		},
		{
			name:     "recovered",
			previous: &base,
			current:  Report{Score: 100},
			want:     true,
		},
		{
			// The detail text moves around as neighbors rotate; on its own that is not
			// worth waking anybody up for.
			name:     "only the detail text differs",
			previous: &Report{Score: 75, Findings: []Finding{{Code: CodeStaleNeighbors, Detail: "links to A"}}},
			current:  Report{Score: 75, Findings: []Finding{{Code: CodeStaleNeighbors, Detail: "links to B"}}},
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := changed(tc.previous, tc.current); got != tc.want {
				t.Errorf("changed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSummarizeListsTheProblemCodes(t *testing.T) {
	if got := summarize(Report{}); got != "no problems" {
		t.Errorf("summarize(clean) = %q, want %q", got, "no problems")
	}

	report := Report{Findings: []Finding{{Code: CodeNoWidget}, {Code: CodeRedirected}}}
	if got := summarize(report); got != "no_widget, redirected" {
		t.Errorf("summarize = %q, want the codes joined", got)
	}
}

// Notifications ship switched off, so enabling the checker on a ring that has never been
// measured does not fire a message for every single member at once.
func TestNotifyIsOffByDefault(t *testing.T) {
	t.Setenv("HEALTH_NOTIFY", "")
	t.Setenv("CHROME_PATH", "")

	cfg, err := LoadConfig()
	if err != nil && err != ErrNoBrowser {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Notify {
		t.Errorf("notifications default to on")
	}
	if cfg.Interval != defaultInterval {
		t.Errorf("interval = %s, want %s", cfg.Interval, defaultInterval)
	}
}

func TestLoadConfigReadsTheInterval(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{value: "30m", want: 30 * time.Minute},
		{value: "2h", want: 2 * time.Hour},
		// Anything under a minute would hammer the members; the default stands instead.
		{value: "1s", want: defaultInterval},
		{value: "nonsense", want: defaultInterval},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("HEALTH_INTERVAL", tc.value)
			cfg, err := LoadConfig()
			if err != nil && err != ErrNoBrowser {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.Interval != tc.want {
				t.Errorf("interval = %s, want %s", cfg.Interval, tc.want)
			}
		})
	}
}

func TestLoadConfigEnablesNotificationsWhenAsked(t *testing.T) {
	t.Setenv("HEALTH_NOTIFY", "true")

	cfg, err := LoadConfig()
	if err != nil && err != ErrNoBrowser {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Notify {
		t.Errorf("HEALTH_NOTIFY=true did not enable notifications")
	}
}
