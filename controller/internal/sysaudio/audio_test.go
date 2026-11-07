package sysaudio

import (
	"fmt"
	"testing"
)

// A runner that answers for the tools named and fails for the rest, so a test can say "this
// machine has pipewire" or "this machine has nothing" exactly.
func only(answers map[string]string) (Runner, *[]string) {
	var calls []string
	return func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+fmt.Sprint(args))
		if out, ok := answers[name]; ok {
			return out, nil
		}
		return "", fmt.Errorf("%s: not found", name)
	}, &calls
}

func TestWpctlIsRead(t *testing.T) {
	run, _ := only(map[string]string{"wpctl": "Volume: 0.45\n"})
	got := Get(run)
	if !got.Available || got.Volume != 45 || got.Muted {
		t.Fatalf("got %+v", got)
	}
}

func TestMuteIsNoticedAndRounding(t *testing.T) {
	run, _ := only(map[string]string{"wpctl": "Volume: 0.73 [MUTED]\n"})
	got := Get(run)
	if !got.Muted || got.Volume != 73 {
		t.Fatalf("got %+v", got)
	}
	// 0.055 is 5.5%, which must not truncate to 5: a volume bar that reads low by a notch at
	// every step is a bar nobody trusts.
	run2, _ := only(map[string]string{"wpctl": "Volume: 0.055\n"})
	if v := Get(run2).Volume; v != 6 {
		t.Fatalf("want 6, got %d", v)
	}
}

func TestOverAmplificationIsReportedNotClamped(t *testing.T) {
	// PipeWire allows over 100%. Reporting 150 as 100 would show a full bar that cannot explain
	// why everything is distorting.
	run, _ := only(map[string]string{"wpctl": "Volume: 1.50\n"})
	if v := Get(run).Volume; v != 150 {
		t.Fatalf("want 150, got %d", v)
	}
}

func TestPactlIsTheFallbackAndMuteCostsASecondCall(t *testing.T) {
	run, calls := only(map[string]string{
		"pactl": "Volume: front-left: 45874 /  70% / -9.33 dB,   front-right: 45874 /  70% / -9.33 dB\n",
	})
	got := Get(run)
	if !got.Available || got.Volume != 70 {
		t.Fatalf("got %+v", got)
	}
	// wpctl was tried first and failed; pactl answered; mute needed its own call.
	if len(*calls) != 3 {
		t.Fatalf("calls: %v", *calls)
	}
}

func TestNothingAnsweringIsNotZeroPercent(t *testing.T) {
	// The whole point of Available. A desktop showing "0%" because it could not find the audio
	// server is making a claim; one showing nothing is not.
	run, _ := only(map[string]string{})
	got := Get(run)
	if got.Available || got.Reason == "" {
		t.Fatalf("got %+v", got)
	}
	if got.Volume != 0 {
		t.Fatalf("volume is meaningless when unavailable, got %d", got.Volume)
	}
}

func TestGarbageIsRefusedRatherThanParsedIntoANumber(t *testing.T) {
	run, _ := only(map[string]string{"wpctl": "Volume: banana\n"})
	if Get(run).Available {
		t.Fatal("unparseable output must not become a volume")
	}
}

func TestSetClampsAndFallsBack(t *testing.T) {
	run, calls := only(map[string]string{"pactl": "ok"})
	if err := Set(run, 500); err != nil {
		t.Fatal(err)
	}
	// Clamped to 150, and pactl used after wpctl failed.
	if (*calls)[1] != "pactl [set-sink-volume @DEFAULT_SINK@ 150%]" {
		t.Fatalf("calls: %v", *calls)
	}
	run2, calls2 := only(map[string]string{"wpctl": "ok"})
	if err := Set(run2, -10); err != nil {
		t.Fatal(err)
	}
	if (*calls2)[0] != "wpctl [set-volume @DEFAULT_AUDIO_SINK@ 0%]" {
		t.Fatalf("calls: %v", *calls2)
	}
}

func TestSetWithNoServerIsAnError(t *testing.T) {
	run, _ := only(map[string]string{})
	if err := Set(run, 50); err == nil {
		t.Fatal("a set that reached nothing must not report success")
	}
}
