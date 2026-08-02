package main

import (
	"strings"
	"testing"
)

func TestValidatorSafetyStatus_UsesRealMinSignedPerWindow(t *testing.T) {
	// window=10000, min_signed_per_window=0.05 (5%) -> a validator only
	// needs to sign 5% of the window to stay safe, a much more lenient bar
	// than the flat 50% used elsewhere. missed=9600 -> only 4% signed,
	// below the 5% bar -> unsafe.
	uptime, safe := validatorSafetyStatus(9600, 10000, 0.05, false)
	if safe {
		t.Fatalf("expected unsafe at 4%% signed against a 5%% min_signed_per_window bar, got safe (uptime=%.2f)", uptime)
	}

	// missed=9400 -> 6% signed, above the 5% bar -> safe, even though this
	// would be "critical" under the flat 50% threshold used by /uptime.
	uptime, safe = validatorSafetyStatus(9400, 10000, 0.05, false)
	if !safe {
		t.Fatalf("expected safe at 6%% signed against a 5%% min_signed_per_window bar, got unsafe (uptime=%.2f)", uptime)
	}
}

func TestValidatorSafetyStatus_FallsBackToCriticalWindowPercent(t *testing.T) {
	// minSignedPerWindow=0 (fetch failed/unavailable) -> falls back to
	// config.CriticalWindowPercent (50), i.e. safe requires >=50% signed.
	_, safe := validatorSafetyStatus(6000, 10000, 0, false)
	if safe {
		t.Fatalf("expected unsafe at 40%% signed under the 50%% fallback threshold, got safe")
	}
	_, safe = validatorSafetyStatus(4000, 10000, 0, false)
	if !safe {
		t.Fatalf("expected safe at 60%% signed under the 50%% fallback threshold, got unsafe")
	}
}

func TestValidatorSafetyStatus_JailedIsAlwaysUnsafe(t *testing.T) {
	// Perfect signing record, but jailed -> must still report unsafe.
	_, safe := validatorSafetyStatus(0, 10000, 0.05, true)
	if safe {
		t.Fatalf("expected jailed validator to be unsafe regardless of uptime")
	}
}

func TestSplitMessage_StaysUnderLimitAndKeepsLinesWhole(t *testing.T) {
	text := "line one\nline two\nline three\nline four\n"
	chunks := splitMessage(text, 20)
	if len(chunks) < 2 {
		t.Fatalf("expected text to be split into multiple chunks, got %d: %v", len(chunks), chunks)
	}
	for _, c := range chunks {
		if len(c) > 20 {
			t.Fatalf("chunk exceeds limit: %q (%d bytes)", c, len(c))
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatalf("chunks do not reassemble to original text: got %q, want %q", strings.Join(chunks, ""), text)
	}
}

func TestSplitMessage_ShortTextIsSingleChunk(t *testing.T) {
	text := "short\ntext\n"
	chunks := splitMessage(text, 3500)
	if len(chunks) != 1 || chunks[0] != text {
		t.Fatalf("expected a single unchanged chunk, got %v", chunks)
	}
}

func TestDecideMissedBlocksAlert_SustainedModerateStreakEscalatesToCritical(t *testing.T) {
	// Check 1: validator goes from 0 -> 25 missed blocks. Below the
	// cumulative critical limit (100) and only 1 check into the streak, so
	// this should be yellow, not critical.
	alerts, level, episodeMissed, streak := decideMissedBlocksAlert(
		"val", 25, 0, true, 0, "", 0, 0,
	)
	if level != "yellow" {
		t.Fatalf("check 1: expected level=yellow, got %q", level)
	}
	if episodeMissed != 25 || streak != 1 {
		t.Fatalf("check 1: expected episodeMissed=25 streak=1, got %d/%d", episodeMissed, streak)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Missed Blocks Alert") {
		t.Fatalf("check 1: expected a yellow alert, got %v", alerts)
	}

	// Check 2: another 25 missed blocks on top (50 -> 75 total episode).
	// Two consecutive checks at/above the yellow limit should force
	// escalation to critical even though cumulative is still under 100.
	alerts, level, episodeMissed, streak = decideMissedBlocksAlert(
		"val", 50, 0, true, 25, level, episodeMissed, streak,
	)
	if level != "critical" {
		t.Fatalf("check 2: expected level=critical, got %q", level)
	}
	if episodeMissed != 50 || streak != 2 {
		t.Fatalf("check 2: expected episodeMissed=50 streak=2, got %d/%d", episodeMissed, streak)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "CRITICAL: Missing Blocks") {
		t.Fatalf("check 2: expected a critical alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_SingleCheckCumulativeCriticalPath(t *testing.T) {
	alerts, level, episodeMissed, streak := decideMissedBlocksAlert(
		"val", 100, 0, true, 0, "", 0, 0,
	)
	if level != "critical" {
		t.Fatalf("expected level=critical, got %q", level)
	}
	if episodeMissed != 100 || streak != 1 {
		t.Fatalf("expected episodeMissed=100 streak=1, got %d/%d", episodeMissed, streak)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "CRITICAL: Missing Blocks") {
		t.Fatalf("expected a critical alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_SmallDipDoesNotRecover(t *testing.T) {
	// Already in yellow with an active episode; a small 5-block dip (well
	// under the 50-block recovery threshold) should not trigger recovery.
	alerts, level, episodeMissed, _ := decideMissedBlocksAlert(
		"val", 20, 0, true, 25, "yellow", 25, 1,
	)
	if level != "yellow" {
		t.Fatalf("expected level to remain yellow, got %q", level)
	}
	if episodeMissed != 25 {
		t.Fatalf("expected episodeMissed to stay at 25, got %d", episodeMissed)
	}
	for _, a := range alerts {
		if strings.Contains(a, "Recovering") {
			t.Fatalf("did not expect a recovery alert from a small dip, got %v", alerts)
		}
	}
}

func TestDecideMissedBlocksAlert_LargeDropRecoversAndResetsEpisode(t *testing.T) {
	// Was critical with a large historical episode total; a single check
	// with a >=50 block drop must recover and clear the episode, even
	// though episodeMissed/streak were both still above their thresholds.
	alerts, level, episodeMissed, streak := decideMissedBlocksAlert(
		"val", 90, 0, true, 150, "critical", 150, 2,
	)
	if level != "" {
		t.Fatalf("expected level to clear, got %q", level)
	}
	if episodeMissed != 0 || streak != 0 {
		t.Fatalf("expected episode state reset, got episodeMissed=%d streak=%d", episodeMissed, streak)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Recovering") {
		t.Fatalf("expected a recovery alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_UptimePercentInMessages(t *testing.T) {
	// window=1000, currentMissedBlocks=20 -> uptime = 98.00%
	alerts, _, _, _ := decideMissedBlocksAlert(
		"val", 20, 1000, true, 0, "", 0, 0,
	)
	if len(alerts) != 1 || !strings.Contains(alerts[0], "98.00%") {
		t.Fatalf("expected message to include 98.00%% uptime, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_NoWindowOmitsUptimeFragment(t *testing.T) {
	alerts, _, _, _ := decideMissedBlocksAlert(
		"val", 20, 0, true, 0, "", 0, 0,
	)
	if len(alerts) != 1 {
		t.Fatalf("expected one alert, got %v", alerts)
	}
	if strings.Contains(alerts[0], "uptime") || strings.Contains(alerts[0], "%%") {
		t.Fatalf("expected no uptime fragment when window is unavailable, got %q", alerts[0])
	}
}
