// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"testing"
	"time"
)

func TestFormatHMSMillis(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "00:00:00.000"},
		{"negative clamps to zero", -5 * time.Second, "00:00:00.000"},
		{"sub-millisecond rounds down", 500 * time.Microsecond, "00:00:00.000"},
		{"one millisecond", time.Millisecond, "00:00:00.001"},
		{"123 ms", 123 * time.Millisecond, "00:00:00.123"},
		{"999 ms", 999 * time.Millisecond, "00:00:00.999"},
		{"one second", time.Second, "00:00:01.000"},
		{"1.500s", 1500 * time.Millisecond, "00:00:01.500"},
		{"59.999s boundary", 59*time.Second + 999*time.Millisecond, "00:00:59.999"},
		{"one minute", time.Minute, "00:01:00.000"},
		{"5m30s", 5*time.Minute + 30*time.Second, "00:05:30.000"},
		{"one hour", time.Hour, "01:00:00.000"},
		{"02:30:05.500", 2*time.Hour + 30*time.Minute + 5*time.Second + 500*time.Millisecond, "02:30:05.500"},
		{"big day-plus", 25*time.Hour + 13*time.Minute + 7*time.Second + 42*time.Millisecond, "25:13:07.042"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FormatHMSMillis(c.in)
			if got != c.want {
				t.Errorf("FormatHMSMillis(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
