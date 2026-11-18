package main

import (
	"reflect"
	"testing"
)

func TestHeadphoneJackEventHandlersSmartPro(t *testing.T) {
	// These are the stock Smart Pro's jack and built-in pad entries. The pad's
	// SW=2 is a different switch and must not be mistaken for headphones.
	const devices = `N: Name="audiocodec sunxi Audio Jack"
H: Handlers=kbd event2
B: EV=23
B: SW=14

N: Name="TRIMUI Player1"
H: Handlers=js0 event3
B: EV=2b
B: SW=2
`
	got := headphoneJackEventHandlers(evdevParseDevices(devices))
	if !reflect.DeepEqual(got, []string{"event2"}) {
		t.Fatalf("jack handlers = %v, want event2 only", got)
	}
}
