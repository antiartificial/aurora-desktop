package sysaudio

import (
	"fmt"
	"strings"
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

// ---- mute -------------------------------------------------------------------

func TestMuteAsksForAStateAndNotAToggle(t *testing.T) {
	// The wire value is 0 or 1, never "toggle". A toggle would make a mute key pressed twice
	// indistinguishable from one press that was lost, and two callers race.
	run, calls := only(map[string]string{"wpctl": "ok"})
	if err := Mute(run, true); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0] != "wpctl [set-mute @DEFAULT_AUDIO_SINK@ 1]" {
		t.Fatalf("calls: %v", *calls)
	}
	run2, calls2 := only(map[string]string{"wpctl": "ok"})
	if err := Mute(run2, false); err != nil {
		t.Fatal(err)
	}
	if (*calls2)[0] != "wpctl [set-mute @DEFAULT_AUDIO_SINK@ 0]" {
		t.Fatalf("calls: %v", *calls2)
	}
}

func TestMuteFallsBackToPactlAndFailsWithNeither(t *testing.T) {
	run, calls := only(map[string]string{"pactl": "ok"})
	if err := Mute(run, true); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1] != "pactl [set-sink-mute @DEFAULT_SINK@ 1]" {
		t.Fatalf("calls: %v", *calls)
	}
	run2, _ := only(map[string]string{})
	if err := Mute(run2, true); err == nil {
		t.Fatal("a mute that reached nothing must not report success")
	}
}

// ---- sinks ------------------------------------------------------------------

// A trimmed-down `pactl -f json list sinks`, with the field shapes this machine's PipeWire
// really produces: an HDMI sink keyed aux0..aux7 and an analog one keyed front-left/front-right.
const twoSinks = `[
 {"index":62,"name":"hdmi","description":"AD102 Pro","mute":false,
  "volume":{"aux0":{"value_percent":"100%"},"aux1":{"value_percent":"100%"},
            "aux2":{"value_percent":"100%"},"aux3":{"value_percent":"100%"}}},
 {"index":146,"name":"analog","description":"Ryzen Analog Stereo","mute":true,
  "volume":{"front-left":{"value_percent":"40%"},"front-right":{"value_percent":"40%"}}}
]`

func sinkRunner(t *testing.T, listing, def string) Runner {
	t.Helper()
	return func(name string, args ...string) (string, error) {
		if name != "pactl" {
			return "", fmt.Errorf("%s: not found", name)
		}
		if len(args) > 0 && args[0] == "get-default-sink" {
			return def + "\n", nil
		}
		return listing, nil
	}
}

func TestSinksAreListedAndTheDefaultIsMarked(t *testing.T) {
	got := Sinks(sinkRunner(t, twoSinks, "analog"))
	if !got.Available || len(got.Sinks) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.Sinks[0].Name != "hdmi" || got.Sinks[0].Default {
		t.Fatalf("sink 0: %+v", got.Sinks[0])
	}
	if !got.Sinks[1].Default || got.Sinks[1].Description != "Ryzen Analog Stereo" || !got.Sinks[1].Muted || got.Sinks[1].Volume != 40 {
		t.Fatalf("sink 1: %+v", got.Sinks[1])
	}
}

func TestPerSinkVolumeIsStableAcrossChannelMapOrder(t *testing.T) {
	// The channel keys are a JSON object, so Go's map iteration order over them is random.
	// Taking "the first" would report a different number on different calls for an unbalanced
	// sink; the loudest channel is the same number every time.
	one := `[{"name":"s","description":"s","mute":false,"volume":{
	  "aux0":{"value_percent":"10%"},"aux1":{"value_percent":"70%"},
	  "aux2":{"value_percent":"30%"},"aux3":{"value_percent":"50%"},
	  "aux4":{"value_percent":"20%"},"aux5":{"value_percent":"60%"}}}]`
	for i := 0; i < 50; i++ {
		got := Sinks(sinkRunner(t, one, ""))
		if got.Sinks[0].Volume != 70 {
			t.Fatalf("run %d reported %d, not the loudest channel", i, got.Sinks[0].Volume)
		}
	}
}

func TestNoDefaultSinkStillListsTheSinks(t *testing.T) {
	// `pactl get-default-sink` failing is not a reason to answer nothing: a list with nothing
	// marked default is still a true list.
	run := func(name string, args ...string) (string, error) {
		if name != "pactl" {
			return "", fmt.Errorf("%s: not found", name)
		}
		if len(args) > 0 && args[0] == "get-default-sink" {
			return "", fmt.Errorf("boom")
		}
		return twoSinks, nil
	}
	got := Sinks(run)
	if !got.Available || len(got.Sinks) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, s := range got.Sinks {
		if s.Default {
			t.Fatalf("%s claims to be default when none was reported", s.Name)
		}
	}
}

func TestNoPactlIsUnavailableAndNotAnEmptyList(t *testing.T) {
	// "no sinks" is a claim about the hardware. A machine with wpctl and no pactl has sinks;
	// what is missing is a listing this can parse, and the reason must say which.
	got := Sinks(func(string, ...string) (string, error) { return "", fmt.Errorf("not found") })
	if got.Available || len(got.Sinks) != 0 || !strings.Contains(got.Reason, "pactl") {
		t.Fatalf("got %+v", got)
	}
	if got.Sinks == nil {
		t.Fatal("sinks must marshal as [] rather than null")
	}
}

func TestSinkGarbageIsRefusedRatherThanParsedIntoAList(t *testing.T) {
	got := Sinks(sinkRunner(t, "Sink #62\n\tName: hdmi\n", ""))
	if got.Available || got.Reason == "" {
		t.Fatalf("non-JSON output must not become a sink list: %+v", got)
	}
}
