package sipserver

import "testing"

func TestBrowserAudioRelayOnlyDoor(t *testing.T) {
	ra, rb := &relay{}, &relay{}
	alarm := &call{from: "1002", to: "1003", relayA: ra, relayB: rb}
	if got := alarm.browserAudioRelay(); got != nil {
		t.Fatalf("alarm call browser mic routed to %p, want nil", got)
	}
	doorCaller := &call{from: "6001", to: "1003", relayA: ra, relayB: rb}
	if got := doorCaller.browserAudioRelay(); got != ra {
		t.Fatalf("door caller browser mic routed to %p, want A leg %p", got, ra)
	}
	doorCallee := &call{from: "1003", to: "6001", relayA: ra, relayB: rb}
	if got := doorCallee.browserAudioRelay(); got != rb {
		t.Fatalf("door callee browser mic routed to %p, want B leg %p", got, rb)
	}
}
