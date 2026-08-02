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

func TestDecideMissedBlocksAlert_BelowNormalLimitNoAlert(t *testing.T) {
	alerts, level := decideMissedBlocksAlert("val", 50, 0, true, 40, "")
	if level != "" {
		t.Fatalf("expected no level, got %q", level)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alert at exactly the normal limit (not exceeding it), got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_AboveNormalLimitAlertsNormal(t *testing.T) {
	alerts, level := decideMissedBlocksAlert("val", 51, 0, true, 40, "")
	if level != "normal" {
		t.Fatalf("expected level=normal, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Missed Blocks Alert") {
		t.Fatalf("expected a normal missed-blocks alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_AtOrAboveCriticalLimitAlertsCritical(t *testing.T) {
	alerts, level := decideMissedBlocksAlert("val", 150, 0, true, 100, "normal")
	if level != "critical" {
		t.Fatalf("expected level=critical, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "CRITICAL: Missing Blocks") {
		t.Fatalf("expected a critical alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_RepeatsEveryCheckWhileAboveThreshold(t *testing.T) {
	// No de-dup: as long as missed blocks stays above the threshold, the
	// alert should fire again on the next check too.
	alerts, level := decideMissedBlocksAlert("val", 200, 0, true, 200, "critical")
	if level != "critical" {
		t.Fatalf("expected level to remain critical, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "CRITICAL: Missing Blocks") {
		t.Fatalf("expected the critical alert to repeat, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_DropBackBelowNormalLimitRecovers(t *testing.T) {
	alerts, level := decideMissedBlocksAlert("val", 30, 0, true, 60, "normal")
	if level != "" {
		t.Fatalf("expected level to clear, got %q", level)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "Recovering") {
		t.Fatalf("expected a recovery alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_NoRecoveryAlertWithoutPriorLevel(t *testing.T) {
	// Never alerted before (level == ""), staying under the threshold should
	// stay silent rather than firing a spurious "recovering" message.
	alerts, level := decideMissedBlocksAlert("val", 10, 0, true, 5, "")
	if level != "" {
		t.Fatalf("expected no level, got %q", level)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_UptimeDropFiresSeparateAlert(t *testing.T) {
	// window=10000: previous uptime 99.50% -> current uptime 98.30%, a
	// 1.2-point drop, above the 1.0-point threshold.
	alerts, _ := decideMissedBlocksAlert("val", 170, 10000, true, 50, "")
	found := false
	for _, a := range alerts {
		if strings.Contains(a, "Uptime Dropping") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an uptime-drop alert, got %v", alerts)
	}
}

func TestDecideMissedBlocksAlert_SmallUptimeDropDoesNotAlert(t *testing.T) {
	// window=10000: previous uptime 99.50% -> current uptime 99.10%, a
	// 0.4-point drop, below the 1.0-point threshold.
	alerts, _ := decideMissedBlocksAlert("val", 90, 10000, true, 50, "")
	for _, a := range alerts {
		if strings.Contains(a, "Uptime Dropping") {
			t.Fatalf("did not expect an uptime-drop alert for a sub-threshold drop, got %v", alerts)
		}
	}
}

func TestDecideMissedBlocksAlert_NoWindowSkipsUptimeDropCheck(t *testing.T) {
	alerts, _ := decideMissedBlocksAlert("val", 90, 0, true, 5, "")
	for _, a := range alerts {
		if strings.Contains(a, "Uptime Dropping") {
			t.Fatalf("did not expect an uptime-drop alert when window is unavailable, got %v", alerts)
		}
	}
}
