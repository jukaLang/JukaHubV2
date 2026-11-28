package main

// juka_pictures.go — handing a picture to Juka from Chat or the viewer.
//
// Juka's eyes are in the AI tab (vision_chat.go): a question carries files,
// and a picture is read from a local path. A community message's picture is
// usually a Discord link, so it is downloaded first; the picture on screen in
// the viewer may be a file in Files or a chat image from the cache. Both paths
// end in chatAskAIWith with one ChatFile, never in a prompt that only
// describes the picture.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// jukaPictureQuestion is what a picture sent without words asks.
const jukaPictureQuestion = "What do you see in this picture?"

// jukaPictureExts are the picture formats the viewer and Juka's eyes share.
var jukaPictureExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
}

// jukaPictureExtForType names a picture's file extension from its MIME type
// when the URL had none (a Discord attachment may be signed with no suffix).
func jukaPictureExtForType(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	}
	return ""
}

// jukaPictureName is the name a downloaded picture is saved under: the URL's
// last segment when it is usable, with an extension from the response's type
// when the name has none.
func jukaPictureName(rawURL, contentType string) string {
	base := ""
	if u, err := parseURLForName(rawURL); err == nil {
		base = path.Base(u)
	}
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	name := chatUploadName(base)
	if name == "file" {
		name = "picture"
	}
	if !jukaPictureExts[strings.ToLower(filepath.Ext(name))] {
		if ext := jukaPictureExtForType(contentType); ext != "" {
			name = strings.TrimSuffix(name, filepath.Ext(name)) + ext
		}
	}
	return name
}

// parseURLForName returns the URL's path without its query or fragment.
func parseURLForName(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Path == "" {
		return "", errors.New("no path")
	}
	return u.Path, nil
}

// jukaPictureFile wraps a picture already on this device.
func jukaPictureFile(path, name string) (ChatFile, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ChatFile{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ChatFile{}, false
	}
	if name = strings.TrimSpace(name); name == "" {
		name = filepath.Base(path)
	}
	return ChatFile{Name: name, Size: info.Size(), Type: chatMIMEType(name), Path: path}, true
}

// chatMessagePictureFile is a message's picture when it is already local (an
// inline file or a saved ImageURL); a remote one returns false and the caller
// downloads it with jukaPictureFromURL.
func chatMessagePictureFile(msg ChatMessage) (ChatFile, bool) {
	if i := chatInlineImageIndex(msg); i >= 0 {
		if f := msg.Files[i]; strings.TrimSpace(f.Path) != "" {
			if pic, ok := jukaPictureFile(f.Path, f.Name); ok {
				return pic, true
			}
		}
	}
	if src := strings.TrimSpace(chatMessageImageURL(msg)); src != "" {
		if pic, ok := jukaPictureFile(src, ""); ok {
			return pic, true
		}
	}
	return ChatFile{}, false
}

// jukaPictureFromURL downloads one picture into the chat save folder and
// returns it as a file Juka's eyes can read. The caller runs this off the
// render thread.
func jukaPictureFromURL(raw string) (ChatFile, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ChatFile{}, errors.New("no picture")
	}
	if pic, ok := jukaPictureFile(raw, ""); ok {
		return pic, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := chatGetFile(ctx, raw)
	if err != nil {
		return ChatFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChatFile{}, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	if resp.ContentLength > chatImageDownloadMaxBytes {
		return ChatFile{}, errors.New("the picture is larger than expected")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, chatImageDownloadMaxBytes+1))
	if err != nil {
		return ChatFile{}, err
	}
	if len(data) == 0 {
		return ChatFile{}, errors.New("the picture was empty")
	}
	if len(data) > chatImageDownloadMaxBytes {
		return ChatFile{}, errors.New("the picture is larger than expected")
	}
	dir := chatSaveDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ChatFile{}, err
	}
	name := jukaPictureName(raw, resp.Header.Get("Content-Type"))
	f, saved, err := createCompanionFile(dir, name)
	if err != nil {
		return ChatFile{}, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return ChatFile{}, err
	}
	if err := f.Close(); err != nil {
		return ChatFile{}, err
	}
	full := filepath.Join(dir, saved)
	chatTrustPath(full)
	return ChatFile{Name: saved, Size: int64(len(data)), Type: chatMIMEType(saved), Path: full}, nil
}

// askJukaWithPicture switches to the AI tab and asks a question that carries
// one picture.
func askJukaWithPicture(config *Config, pic ChatFile, question string) {
	if strings.TrimSpace(question) == "" {
		question = jukaPictureQuestion
	}
	if chatActiveTabID() != chatTabAI {
		chatSelectTab(chatTabAI)
	}
	if !chatAskAIWith(question, []ChatFile{pic}) {
		return
	}
	chatJumpToLatest()
	chatReturnToChat(config)
}

// chatAskJukaAboutMessage asks Juka about a community message's picture,
// downloading a Discord link first. It runs the download on its own goroutine
// and hands the result back on the main thread.
func chatAskJukaAboutMessage(config *Config, msg ChatMessage, question string) {
	if pic, ok := chatMessagePictureFile(msg); ok {
		askJukaWithPicture(config, pic, question)
		return
	}
	raw := strings.TrimSpace(chatMessageImageURL(msg))
	if raw == "" {
		showToast("This message has no picture to show Juka", ColorToastWarn)
		return
	}
	ShowToast("Getting the picture for Juka…", ToastKindInfo)
	go func() {
		pic, err := jukaPictureFromURL(raw)
		tbxPost(func() {
			if err != nil {
				ShowToast("Could not get the picture: "+firstLine(err.Error()), ToastKindWarning)
				return
			}
			askJukaWithPicture(config, pic, question)
		})
	}()
}

// imgvAskJuka sends the picture on screen to Juka, downloading a chat image
// that only exists as a URL first.
func imgvAskJuka(config *Config) {
	raw := strings.TrimSpace(imageViewerPath)
	if raw == "" {
		return
	}
	if imageViewerRemote {
		ShowToast("Getting the picture for Juka…", ToastKindInfo)
		go func() {
			pic, err := jukaPictureFromURL(raw)
			tbxPost(func() {
				if err != nil {
					ShowToast("Could not get the picture: "+firstLine(err.Error()), ToastKindWarning)
					return
				}
				closeImageViewer()
				askJukaWithPicture(config, pic, jukaPictureQuestion)
			})
		}()
		return
	}
	pic, ok := jukaPictureFile(raw, filepath.Base(raw))
	if !ok {
		ShowToast("That picture can't be read", ToastKindWarning)
		return
	}
	closeImageViewer()
	askJukaWithPicture(config, pic, jukaPictureQuestion)
}
