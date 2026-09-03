// Package schedule is the platform seam for unattended execution (TDD 7.2). It
// generates scheduler artifacts (macOS launchd, Linux cron) and carries the
// only OS-specific knowledge in the codebase, so the core stays
// platform-independent and the same binary runs under Linux cron unchanged.
//
// This package produces artifact text only; it never installs or activates a
// schedule itself. It imports nothing from the pipeline packages, keeping the
// seam one-directional.
package schedule

import (
	"fmt"
	"sort"
	"strings"
)

// ClockTime is a wall-clock hour:minute the job fires at, in the machine's local
// time (launchd and cron both match on local wall-clock time).
type ClockTime struct {
	Hour   int // 0–23
	Minute int // 0–59
}

// Spec describes when and how to run the binary unattended.
type Spec struct {
	// Label identifies the scheduled job (launchd label / cron comment).
	Label string
	// BinaryPath is the absolute path to the deployed binary (or a wrapper that
	// sets up the environment and execs it).
	BinaryPath string
	// Args are additional program arguments passed after BinaryPath.
	Args []string

	// Hour and Minute give a single daily run time (0–23, 0–59). Used only when
	// Times is empty, preserving the simple daily-run form.
	Hour   int
	Minute int

	// Times and Weekdays describe a recurring windowed schedule: the job fires
	// at every ClockTime in Times, on every weekday in Weekdays (0=Sunday …
	// 6=Saturday). When Times is non-empty it supersedes Hour/Minute and the
	// launchd plist emits a StartCalendarInterval array of weekday×time entries.
	// When Weekdays is empty the times fire every day.
	Times    []ClockTime
	Weekdays []int

	// EnvironmentVariables are injected into the job's environment. A launchd
	// job does not inherit an interactive shell's environment, so PATH and any
	// GSB_* settings the run needs must be supplied here.
	EnvironmentVariables map[string]string

	// StdoutPath / StderrPath, when set, capture the run's output.
	StdoutPath string
	StderrPath string
}

// WindowedTimes returns the clock times starting at (startHour:startMinute),
// spaced stepMinutes apart, up to and including (endHour:endMinute). It is the
// helper for "every N minutes within a daily window" schedules.
func WindowedTimes(startHour, startMinute, endHour, endMinute, stepMinutes int) []ClockTime {
	if stepMinutes <= 0 {
		return nil
	}
	start := startHour*60 + startMinute
	end := endHour*60 + endMinute
	var out []ClockTime
	for m := start; m <= end; m += stepMinutes {
		out = append(out, ClockTime{Hour: m / 60, Minute: m % 60})
	}
	return out
}

// calendarEntries expands the schedule into the concrete (weekday, time) pairs
// launchd's StartCalendarInterval array holds. A weekday of -1 means "every
// day" (no Weekday key emitted).
func (s Spec) calendarEntries() []struct {
	Weekday int
	Time    ClockTime
} {
	times := s.Times
	if len(times) == 0 {
		times = []ClockTime{{Hour: s.Hour, Minute: s.Minute}}
	}
	weekdays := s.Weekdays
	if len(weekdays) == 0 {
		weekdays = []int{-1} // every day
	}
	var out []struct {
		Weekday int
		Time    ClockTime
	}
	for _, wd := range weekdays {
		for _, t := range times {
			out = append(out, struct {
				Weekday int
				Time    ClockTime
			}{wd, t})
		}
	}
	return out
}

// LaunchdPlist renders a macOS launchd plist that runs the binary on the
// configured schedule (MVP scheduler). With a single daily time it emits a
// StartCalendarInterval dict; with Times set it emits a StartCalendarInterval
// array of weekday×time entries.
func (s Spec) LaunchdPlist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	b.WriteString("  <key>Label</key>\n")
	b.WriteString(fmt.Sprintf("  <string>%s</string>\n", s.Label))

	b.WriteString("  <key>ProgramArguments</key>\n")
	b.WriteString("  <array>\n")
	b.WriteString(fmt.Sprintf("    <string>%s</string>\n", s.BinaryPath))
	for _, a := range s.Args {
		b.WriteString(fmt.Sprintf("    <string>%s</string>\n", a))
	}
	b.WriteString("  </array>\n")

	if len(s.EnvironmentVariables) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n")
		b.WriteString("  <dict>\n")
		// Deterministic key order for a stable, diffable plist.
		keys := make([]string, 0, len(s.EnvironmentVariables))
		for k := range s.EnvironmentVariables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("    <key>%s</key>\n", k))
			b.WriteString(fmt.Sprintf("    <string>%s</string>\n", s.EnvironmentVariables[k]))
		}
		b.WriteString("  </dict>\n")
	}

	entries := s.calendarEntries()
	b.WriteString("  <key>StartCalendarInterval</key>\n")
	if len(entries) == 1 && entries[0].Weekday == -1 {
		// Single daily time: a plain dict keeps the simple form readable.
		b.WriteString("  <dict>\n")
		b.WriteString(fmt.Sprintf("    <key>Hour</key><integer>%d</integer>\n", entries[0].Time.Hour))
		b.WriteString(fmt.Sprintf("    <key>Minute</key><integer>%d</integer>\n", entries[0].Time.Minute))
		b.WriteString("  </dict>\n")
	} else {
		b.WriteString("  <array>\n")
		for _, e := range entries {
			b.WriteString("    <dict>\n")
			if e.Weekday >= 0 {
				b.WriteString(fmt.Sprintf("      <key>Weekday</key><integer>%d</integer>\n", e.Weekday))
			}
			b.WriteString(fmt.Sprintf("      <key>Hour</key><integer>%d</integer>\n", e.Time.Hour))
			b.WriteString(fmt.Sprintf("      <key>Minute</key><integer>%d</integer>\n", e.Time.Minute))
			b.WriteString("    </dict>\n")
		}
		b.WriteString("  </array>\n")
	}

	if s.StdoutPath != "" {
		b.WriteString("  <key>StandardOutPath</key>\n")
		b.WriteString(fmt.Sprintf("  <string>%s</string>\n", s.StdoutPath))
	}
	if s.StderrPath != "" {
		b.WriteString("  <key>StandardErrorPath</key>\n")
		b.WriteString(fmt.Sprintf("  <string>%s</string>\n", s.StderrPath))
	}
	b.WriteString("  <key>RunAtLoad</key>\n")
	b.WriteString("  <false/>\n")
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

// CronLine renders Linux crontab lines that run the binary on the configured
// schedule (supported target with no core changes). A windowed/weekday schedule
// emits one crontab line covering the times and days; a single daily time emits
// the simple form.
func (s Spec) CronLine() string {
	redirect := ""
	if s.StdoutPath != "" {
		redirect += fmt.Sprintf(" >> %s", s.StdoutPath)
	}
	if s.StderrPath != "" {
		redirect += fmt.Sprintf(" 2>> %s", s.StderrPath)
	}
	cmd := s.BinaryPath
	if len(s.Args) > 0 {
		cmd += " " + strings.Join(s.Args, " ")
	}

	if len(s.Times) == 0 {
		// Simple daily form (unchanged).
		return fmt.Sprintf("# %s\n%d %d * * * %s%s\n", s.Label, s.Minute, s.Hour, cmd, redirect)
	}

	// Windowed form: cron cannot space by an arbitrary N minutes across the
	// hour, so enumerate the exact minute/hour pairs as separate fields is not
	// possible in one 5-field line; emit one line per distinct time, restricted
	// to the weekday set.
	dow := "*"
	if len(s.Weekdays) > 0 {
		parts := make([]string, len(s.Weekdays))
		for i, wd := range s.Weekdays {
			parts[i] = fmt.Sprintf("%d", wd)
		}
		dow = strings.Join(parts, ",")
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s\n", s.Label))
	for _, t := range s.Times {
		b.WriteString(fmt.Sprintf("%d %d * * %s %s%s\n", t.Minute, t.Hour, dow, cmd, redirect))
	}
	return b.String()
}
