package runner_test

import (
	"errors"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
)

func run(status runner.TestStatus, d time.Duration) *runner.TestRun {
	start := time.Now()
	return &runner.TestRun{
		Test:      &discovery.DiscoveredFile{RelativePath: "x_test.sql"},
		StartTime: start,
		EndTime:   start.Add(d),
		Status:    status,
		Error:     errors.New("boom"),
	}
}

func TestSummarizeRuns(t *testing.T) {
	tests := []struct {
		name                            string
		runs                            []*runner.TestRun
		total, passed, failed, timedOut int
		allPassed                       bool
		exitCode                        int
	}{
		{
			name:      "no runs is a pass",
			runs:      nil,
			allPassed: true,
			exitCode:  0,
		},
		{
			name:      "all passed",
			runs:      []*runner.TestRun{run(runner.TestPassed, time.Second), run(runner.TestPassed, time.Second)},
			total:     2,
			passed:    2,
			allPassed: true,
			exitCode:  0,
		},
		{
			name:      "one failure",
			runs:      []*runner.TestRun{run(runner.TestPassed, time.Second), run(runner.TestFailed, time.Second)},
			total:     2,
			passed:    1,
			failed:    1,
			allPassed: false,
			exitCode:  1,
		},
		{
			name:      "a timeout is counted separately and still fails the run",
			runs:      []*runner.TestRun{run(runner.TestPassed, time.Second), run(runner.TestTimeout, time.Second)},
			total:     2,
			passed:    1,
			timedOut:  1,
			allPassed: false,
			exitCode:  1,
		},
		{
			name:      "pending runs count toward the total only",
			runs:      []*runner.TestRun{run(runner.TestPending, time.Second)},
			total:     1,
			allPassed: true,
			exitCode:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := runner.SummarizeRuns(tc.runs)
			if s.TotalTests != tc.total {
				t.Errorf("TotalTests = %d, want %d", s.TotalTests, tc.total)
			}
			if s.PassedTests != tc.passed {
				t.Errorf("PassedTests = %d, want %d", s.PassedTests, tc.passed)
			}
			if s.FailedTests != tc.failed {
				t.Errorf("FailedTests = %d, want %d", s.FailedTests, tc.failed)
			}
			if s.TimedOutTests != tc.timedOut {
				t.Errorf("TimedOutTests = %d, want %d", s.TimedOutTests, tc.timedOut)
			}
			if s.AllPassed() != tc.allPassed {
				t.Errorf("AllPassed() = %v, want %v", s.AllPassed(), tc.allPassed)
			}
			if s.ExitCode() != tc.exitCode {
				t.Errorf("ExitCode() = %d, want %d", s.ExitCode(), tc.exitCode)
			}
		})
	}
}

func TestSummarizeRuns_AccumulatesDuration(t *testing.T) {
	s := runner.SummarizeRuns([]*runner.TestRun{
		run(runner.TestPassed, 100*time.Millisecond),
		run(runner.TestPassed, 250*time.Millisecond),
	})
	if want := 350 * time.Millisecond; s.TotalDuration != want {
		t.Errorf("TotalDuration = %v, want %v", s.TotalDuration, want)
	}
}

func TestTestRunDuration_UsesNowWhileRunning(t *testing.T) {
	r := &runner.TestRun{StartTime: time.Now().Add(-50 * time.Millisecond)}
	if d := r.Duration(); d < 50*time.Millisecond {
		t.Errorf("Duration() = %v; an unfinished run must measure against now", d)
	}
}

func TestTestStatusString(t *testing.T) {
	cases := map[runner.TestStatus]string{
		runner.TestPending:    "pending",
		runner.TestRunning:    "running",
		runner.TestPassed:     "passed",
		runner.TestFailed:     "failed",
		runner.TestTimeout:    "timeout",
		runner.TestStatus(99): "unknown",
	}
	for status, want := range cases {
		if got := status.String(); got != want {
			t.Errorf("TestStatus(%d).String() = %q, want %q", status, got, want)
		}
	}
}
