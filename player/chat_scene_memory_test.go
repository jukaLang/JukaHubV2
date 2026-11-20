package main

import "testing"

func TestChatSceneReleaseMemoryKeepsOnlyOpenViewerImage(t *testing.T) {
	oldPath, oldRemote := imageViewerPath, imageViewerRemote
	oldConfig := appConfig
	t.Cleanup(func() {
		clearChatImageCache()
		imageViewerPath, imageViewerRemote = oldPath, oldRemote
		appConfig = oldConfig
	})

	chatImageMutex.Lock()
	chatImages.clear()
	chatImages.put("viewer", chatImageTexture{bytes: 4})
	chatImages.put("other", chatImageTexture{bytes: 8})
	chatImageMutex.Unlock()

	imageViewerPath, imageViewerRemote = "viewer", true
	chatSceneReleaseMemory()
	chatImageMutex.Lock()
	entries, bytes := chatImages.stats()
	_, kept := chatImages.get("viewer")
	_, discarded := chatImages.get("other")
	chatImageMutex.Unlock()
	if entries != 1 || bytes != 4 || !kept || discarded {
		t.Fatalf("viewer cache after Chat exit: entries=%d bytes=%d kept=%v discarded=%v", entries, bytes, kept, discarded)
	}

	appConfig = nil // Closing outside Chat should release the viewer's last image.
	closeImageViewer()
	if entries, bytes := chatImageCacheStats(); entries != 0 || bytes != 0 {
		t.Fatalf("chat images after viewer closes: entries=%d bytes=%d", entries, bytes)
	}
}
