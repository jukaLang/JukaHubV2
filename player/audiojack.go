package main

// headphoneJackEventHandlers finds input devices whose switch bitmap includes
// a headphone or line-out jack. The stock Smart Pro exposes the jack as
// "audiocodec sunxi Audio Jack" on event2, with B: SW=14, and has neither a
// switch-class nor an extcon state file. Its virtual gamepad also has a switch
// bit, so the specific jack capability matters more than the device name.
func headphoneJackEventHandlers(devices []evdevDevice) []string {
	var handlers []string
	for _, d := range devices {
		if d.Handler == "" || !evdevHas(d.EV, evdevEvSw) {
			continue
		}
		if evdevHas(d.SW, evdevSwHeadphoneInsert) || evdevHas(d.SW, evdevSwLineoutInsert) {
			handlers = append(handlers, d.Handler)
		}
	}
	return handlers
}
