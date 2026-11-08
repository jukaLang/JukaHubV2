package main

// files_input.go — the mouse input the Files surfaces share: the wheel and
// dragging. The main loop hands both here first, so the picture viewer, the
// text page, the PDF reader and the Files list each get the gesture that fits
// them (zoom, scroll, pan, page) instead of nothing.

// filesHandleWheel routes one wheel event (notches > 0 is away from the user)
// to the Files surface on screen. It reports whether the wheel was used.
func filesHandleWheel(config *Config, notches int32) bool {
	if virtualKeyboardActive || tb.startOpen || nc.open || qs.open {
		return false
	}
	switch {
	case imageViewerPath != "":
		return imgvWheel(notches)
	case fileEditor.open:
		return fileEditorWheel(notches)
	case pdfvSceneActive(config):
		return pdfvWheel(notches)
	case feSceneActive(config):
		if feSheet.open || feRemotePanelOnScreen(config) {
			return false
		}
		if notches != 0 {
			filesPageList(config, int(-notches)*3)
		}
		return true
	}
	return false
}

// filesMouseMotion pans a picture or a page while the left button is held.
func filesMouseMotion(config *Config, mx, my int32, leftHeld bool) {
	switch {
	case imageViewerPath != "":
		imgvMouseMotion(mx, my, leftHeld)
	case pdfvSceneActive(config):
		pdfvMouseMotion(mx, my, leftHeld)
	}
}

// filesMouseUp ends a drag.
func filesMouseUp(config *Config) {
	switch {
	case imageViewerPath != "":
		imgvMouseUp()
	case pdfvSceneActive(config):
		pdfvMouseUp()
	}
}
