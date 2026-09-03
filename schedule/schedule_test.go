package schedule

import (
	"strings"
	"testing"
)

func spec() Spec {
	return Spec{Label: "com.example.gsb", BinaryPath: "/usr/local/bin/githubslashboard", Hour: 7, Minute: 30, StdoutPath: "/tmp/gsb.out", StderrPath: "/tmp/gsb.err"}
}

func TestLaunchdPlist(t *testing.T) {
	p := spec().LaunchdPlist()
	for _, want := range []string{"com.example.gsb", "/usr/local/bin/githubslashboard", "<integer>7</integer>", "<integer>30</integer>", "/tmp/gsb.out"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
}

func TestCronLine(t *testing.T) {
	c := spec().CronLine()
	if !strings.Contains(c, "30 7 * * * /usr/local/bin/githubslashboard") {
		t.Errorf("cron line wrong:\n%s", c)
	}
	if !strings.Contains(c, ">> /tmp/gsb.out") || !strings.Contains(c, "2>> /tmp/gsb.err") {
		t.Errorf("cron redirects missing:\n%s", c)
	}
}

func TestWindowedTimes(t *testing.T) {
	got := WindowedTimes(6, 0, 15, 0, 45)
	// 06:00 to 15:00 every 45 min, inclusive of 15:00.
	if len(got) != 13 {
		t.Fatalf("got %d times, want 13: %+v", len(got), got)
	}
	if got[0] != (ClockTime{6, 0}) {
		t.Errorf("first = %+v, want 06:00", got[0])
	}
	if got[len(got)-1] != (ClockTime{15, 0}) {
		t.Errorf("last = %+v, want 15:00", got[len(got)-1])
	}
	// The 45-min drift: second fire is 06:45, third 07:30.
	if got[1] != (ClockTime{6, 45}) || got[2] != (ClockTime{7, 30}) {
		t.Errorf("spacing wrong: %+v", got[:3])
	}
}

func windowedSpec() Spec {
	return Spec{
		Label:      "com.improving.example.githubslashboard",
		BinaryPath: "/Users/example/.local/bin/githubslashboard",
		Times:      WindowedTimes(6, 0, 15, 0, 45),
		Weekdays:   []int{1, 2, 3, 4, 5}, // Mon–Fri
		EnvironmentVariables: map[string]string{
			"PATH":            "/opt/homebrew/bin:/usr/bin:/bin",
			"GSB_OUTPUT_PATH": "/Users/example/reports/GSB-SlashBoard.md",
		},
		StdoutPath: "/tmp/o.log",
		StderrPath: "/tmp/e.log",
	}
}

func TestLaunchdPlistWindowedArray(t *testing.T) {
	p := windowedSpec().LaunchdPlist()

	// StartCalendarInterval must be an array (not a single dict) for the
	// recurring windowed schedule.
	if !strings.Contains(p, "<key>StartCalendarInterval</key>\n  <array>") {
		t.Errorf("expected StartCalendarInterval array:\n%s", p)
	}
	// 13 times × 5 weekdays = 65 entries; each has a Weekday key.
	if got := strings.Count(p, "<key>Weekday</key>"); got != 65 {
		t.Errorf("weekday entries = %d, want 65", got)
	}
	if got := strings.Count(p, "<key>Hour</key>"); got != 65 {
		t.Errorf("hour entries = %d, want 65", got)
	}
	// Window bounds present.
	if !strings.Contains(p, "<key>Hour</key><integer>6</integer>") ||
		!strings.Contains(p, "<key>Hour</key><integer>15</integer>") {
		t.Errorf("window bounds missing:\n%s", p)
	}
	// Weekend must not appear (Weekday 0 = Sunday, 6 = Saturday).
	if strings.Contains(p, "<key>Weekday</key><integer>0</integer>") ||
		strings.Contains(p, "<key>Weekday</key><integer>6</integer>") {
		t.Errorf("weekend weekday present, want weekdays only:\n%s", p)
	}
	// Environment variables injected.
	if !strings.Contains(p, "<key>EnvironmentVariables</key>") ||
		!strings.Contains(p, "<key>GSB_OUTPUT_PATH</key>") ||
		!strings.Contains(p, "/Users/example/reports/GSB-SlashBoard.md") ||
		!strings.Contains(p, "<key>PATH</key>") {
		t.Errorf("environment variables missing:\n%s", p)
	}
}

func TestLaunchdPlistArgs(t *testing.T) {
	s := windowedSpec()
	s.Args = []string{"-verbose"}
	p := s.LaunchdPlist()
	if !strings.Contains(p, "<string>-verbose</string>") {
		t.Errorf("args missing from ProgramArguments:\n%s", p)
	}
}

func TestCronLineWindowed(t *testing.T) {
	c := windowedSpec().CronLine()
	// One line per time, restricted to Mon–Fri (1,2,3,4,5).
	if !strings.Contains(c, "0 6 * * 1,2,3,4,5 /Users/example/.local/bin/githubslashboard") {
		t.Errorf("windowed cron first line wrong:\n%s", c)
	}
	if !strings.Contains(c, "0 15 * * 1,2,3,4,5 /Users/example/.local/bin/githubslashboard") {
		t.Errorf("windowed cron last line wrong:\n%s", c)
	}
	// 13 crontab lines (one per time) plus the comment.
	lines := 0
	for _, ln := range strings.Split(strings.TrimSpace(c), "\n") {
		if strings.HasPrefix(ln, "#") || strings.TrimSpace(ln) == "" {
			continue
		}
		lines++
	}
	if lines != 13 {
		t.Errorf("cron lines = %d, want 13", lines)
	}
}
