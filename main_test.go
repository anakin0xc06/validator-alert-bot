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

// Regression test for a real production report: a validator sat around
// ~2700-2760 missed blocks (out of a 108000 window, ~97.5% uptime) while
// the count was actually *decreasing* check over check, but the old
// absolute-threshold logic (missed >= 150) kept re-firing a critical alert
// every single check regardless of trend. With delta/threshold-based
// logic, a shrinking count on an already-healthy window should be silent.
func TestDecideMissedBlocksAlert_ShrinkingMissedBlocksOnHealthyWindowDoesNotRepeatCritical(t *testing.T) {
	alerts, level := decideMissedBlocksAlert("Citadel.one (sei)", 2702, 108000, true, 2760, "critical")
	for _, a := range alerts {
		if strings.Contains(a, "CRITICAL") {
			t.Fatalf("did not expect a(nother) critical alert while missed blocks shrink on an otherwise-healthy window, got %v", alerts)
		}
	}
	// Neither the critical nor normal conditions hold anymore, so at most a
	// single one-off recovery message is acceptable (clearing the level) —
	// what must NOT happen is another repeat of the critical alert.
	if len(alerts) > 1 {
		t.Fatalf("expected at most one alert (a recovery message), got %v", alerts)
	}
	if level != "" {
		t.Fatalf("expected level to clear once neither condition holds, got %q", level)
	}
}

func TestDecideMissedBlocksAlert_DeltaOverCriticalThresholdIsCritical(t *testing.T) {
	// delta = 250 - 40 = 210, over the 100 critical threshold
	alerts, level := decideMissedBlocksAlert("val", 250, 0, true, 40, "")
	if level != "critical" {
		t.Fatalf("expected level=critical, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "CRITICAL: Missing Blocks") {
		t.Fatalf("expected a critical alert, got %v", alerts)
	}
	if !strings.Contains(alerts[0], "increased by 210") {
		t.Fatalf("expected the alert to explain the delta, got %v", alerts[0])
	}
}

func TestDecideMissedBlocksAlert_DeltaExactlyAtCriticalThresholdIsNormalNotCritical(t *testing.T) {
	// "critical if increased over 100" -> exactly 100 is NOT over 100
	alerts, level := decideMissedBlocksAlert("val", 140, 0, true, 40, "")
	if level != "normal" {
		t.Fatalf("expected level=normal at exactly the boundary, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Missed Blocks Alert") {
		t.Fatalf("expected a normal alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_DeltaInNormalRangeIsNormal(t *testing.T) {
	// delta = 70, within the 50-100 normal range
	alerts, level := decideMissedBlocksAlert("val", 110, 0, true, 40, "")
	if level != "normal" {
		t.Fatalf("expected level=normal, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Missed Blocks Alert") {
		t.Fatalf("expected a normal alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_DeltaBelowNormalRangeStaysSilent(t *testing.T) {
	// delta = 30, under the 50 floor, and uptime unavailable (window=0)
	alerts, level := decideMissedBlocksAlert("val", 70, 0, true, 40, "")
	if len(alerts) != 0 {
		t.Fatalf("expected no alert for a small delta with no uptime signal, got %v", alerts)
	}
	if level != "" {
		t.Fatalf("expected no level, got %q", level)
	}
}

func TestDecideMissedBlocksAlert_AbsoluteUptimeBelow80IsCriticalRegardlessOfDelta(t *testing.T) {
	// window=1000, missed=250 -> uptime=75%, below the 80% critical floor,
	// even though the delta itself (10) is tiny
	alerts, level := decideMissedBlocksAlert("val", 250, 1000, true, 240, "")
	if level != "critical" {
		t.Fatalf("expected level=critical, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "CRITICAL: Missing Blocks") {
		t.Fatalf("expected a critical alert, got %v", alerts)
	}
	if !strings.Contains(alerts[0], "uptime is below 80%") {
		t.Fatalf("expected the alert to explain the uptime reason, got %v", alerts[0])
	}
}

func TestDecideMissedBlocksAlert_AbsoluteUptimeCriticalFiresOnFirstCheckEvenWithoutPrevious(t *testing.T) {
	// A validator discovered already deep in trouble should be flagged
	// immediately, not wait for a second reading to establish a delta.
	alerts, level := decideMissedBlocksAlert("val", 500, 1000, false, 0, "")
	if level != "critical" {
		t.Fatalf("expected level=critical, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "CRITICAL: Missing Blocks") {
		t.Fatalf("expected a critical alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_UptimeDropTriggersNormalAlert(t *testing.T) {
	// window=3000, delta=35 (well under the 50 delta floor, so the delta
	// condition alone would stay silent): previous uptime 99.67% -> current
	// uptime 98.50%, a ~1.17-point drop, above the 1.0-point threshold.
	alerts, level := decideMissedBlocksAlert("val", 45, 3000, true, 10, "")
	if level != "normal" {
		t.Fatalf("expected level=normal, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Uptime dropped 1.17 points") {
		t.Fatalf("expected the alert to explain the uptime drop, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_SmallUptimeDropAndSmallDeltaStaysSilent(t *testing.T) {
	// window=10000: previous uptime 99.50% -> current uptime 99.10%, a
	// 0.4-point drop, below the 1.0-point threshold; delta=40 is also under
	// the 50 floor.
	alerts, level := decideMissedBlocksAlert("val", 90, 10000, true, 50, "")
	if len(alerts) != 0 {
		t.Fatalf("expected no alert for a sub-threshold uptime drop and delta, got %v", alerts)
	}
	if level != "" {
		t.Fatalf("expected no level, got %q", level)
	}
}

func TestDecideMissedBlocksAlert_RecoversWhenNoConditionHoldsAnymore(t *testing.T) {
	alerts, level := decideMissedBlocksAlert("val", 42, 10000, true, 45, "normal")
	if level != "" {
		t.Fatalf("expected level to clear, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Recovering") {
		t.Fatalf("expected a recovery alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_NoRecoveryAlertWithoutPriorLevel(t *testing.T) {
	alerts, level := decideMissedBlocksAlert("val", 10, 0, true, 5, "")
	if level != "" {
		t.Fatalf("expected no level, got %q", level)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_MessagesShowUptimeAndMissedCount(t *testing.T) {
	alerts, _ := decideMissedBlocksAlert("val", 250, 1000, true, 40, "")
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %v", alerts)
	}
	if !strings.Contains(alerts[0], "Missed blocks: *250*") {
		t.Fatalf("expected the message to show the current missed-blocks count, got %v", alerts[0])
	}
	if !strings.Contains(alerts[0], "Uptime: *75.00%*") {
		t.Fatalf("expected the message to show the current uptime percentage, got %v", alerts[0])
	}
}
