package main

// media_hub_favs.go — removing a favourite from the Media screen. On the
// Favorites tab X (or Delete, or the bin on the focused card, or the details
// sheet's Remove from Favorites) takes the video off at once; an Undo bar
// offers it back, in its place, for a few seconds (Y, U, Ctrl+Z or a tap);
// and a video that is also downloaded asks whether its file goes too — the
// favourite and the download are separate things the user may want apart.

import (
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// mediaFavUndoWindow is how long a removal can be taken back.
const mediaFavUndoWindow = 6 * time.Second

// mediaFavUndo is the last favourite removed here: the item, its place in the
// saved list, and the bar's text, built once.
var mediaFavUndo struct {
	item  FavoriteItem
	index int
	title string
	label string
	at    time.Time
}

// mediaFavUndoActive reports whether the last removal can still be undone.
func mediaFavUndoActive() bool {
	return !mediaFavUndo.at.IsZero() && time.Since(mediaFavUndo.at) <= mediaFavUndoWindow
}

// mediaFavUndoRestart gives the Undo bar its whole time again once the
// question about the download is answered.
func mediaFavUndoRestart() {
	if !mediaFavUndo.at.IsZero() {
		mediaFavUndo.at = time.Now()
	}
}

// mediaFavTake removes the saved video with this link and returns it with its
// place in the list.
func mediaFavTake(url string) (FavoriteItem, int, bool) {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for i, it := range favoritesStore.Videos {
		v, ok := it.Data.(VideoInfo)
		if !ok || strings.TrimSpace(v.GetURL()) != url {
			continue
		}
		out := make([]FavoriteItem, 0, len(favoritesStore.Videos)-1)
		out = append(out, favoritesStore.Videos[:i]...)
		out = append(out, favoritesStore.Videos[i+1:]...)
		favoritesStore.Videos = out
		return it, i, true
	}
	return FavoriteItem{}, -1, false
}

// mediaHubRemoveFavorite takes a card's video off Favorites, offers it back,
// and asks about a download of it. It reports whether anything was removed.
func mediaHubRemoveFavorite(card mediaCard) bool {
	url := strings.TrimSpace(card.video.GetURL())
	if url == "" {
		return false
	}
	item, index, ok := mediaFavTake(url)
	if !ok {
		return false
	}
	saveFavorites()
	PlayBackSound()
	title := dlFirstNonEmpty(card.title, card.video.Title, url)
	mediaFavUndo.item = item
	mediaFavUndo.index = index
	mediaFavUndo.title = title
	mediaFavUndo.label = "Removed “" + dlShortTitle(title, 60) + "” from Favorites"
	mediaFavUndo.at = time.Now()
	mediaHub.builtAt = 0
	if e, found := dlLibraryFindSource(url, true); found {
		mediaHub.confirm = mediaConfirm{
			open:   true,
			kind:   mediaConfirmFavDownload,
			libID:  e.ID,
			title:  "Also delete the download?",
			body:   "“" + dlShortTitle(dlFirstNonEmpty(e.Title, title), 50) + "” is downloaded (" + humanBytes(e.Size) + "). Delete the file too, or keep it in Downloads.",
			ok:     "Delete file",
			cancel: "Keep file",
			focus:  0,
			freed:  e.Size,
			back:   mediaHub.zone,
		}
	}
	return true
}

// mediaHubUndoFavorite puts the last removed favourite back where it was.
func mediaHubUndoFavorite() bool {
	if !mediaFavUndoActive() {
		return false
	}
	u := mediaFavUndo
	mediaFavUndo.at = time.Time{}
	mediaFavUndo.item = FavoriteItem{}
	favoritesMutex.Lock()
	favoritesStore.Videos = favoritesInsertAt(favoritesStore.Videos, u.index, u.item)
	favoritesMutex.Unlock()
	saveFavorites()
	PlayActivateSound()
	ShowToast("Back in Favorites: "+dlShortTitle(u.title, 48), ToastKindSuccess)
	mediaHub.builtAt = 0
	return true
}

// mediaHubDrawFavUndo paints the "Removed … · Undo" bar along the bottom of the
// Favorites list while the removal can be taken back.
func mediaHubDrawFavUndo(renderer *sdl.Renderer, config *Config, list sdl.Rect) {
	if !mediaFavUndoActive() || mediaHub.confirm.open {
		return
	}
	font := getFontSized(config, "small", 16)
	if font == nil {
		return
	}
	w := list.W - 40
	if w > 680 {
		w = 680
	}
	h := int32(48)
	bar := sdl.Rect{X: list.X + (list.W-w)/2, Y: list.Y + list.H - h - 6, W: w, H: h}
	drawGlassSheet(renderer, bar, glassRaisedFill())
	label := "Undo"
	bw, bh := measureText(font, label)
	btn := sdl.Rect{X: bar.X + bar.W - bw - 78, Y: bar.Y + 7, W: bw + 64, H: bar.H - 14}
	fillRoundedRect(renderer, btn.X, btn.Y, btn.W, btn.H, btn.H/2, WithAlpha(glassAccent(), 60))
	strokeRoundedRect(renderer, btn.X, btn.Y, btn.W, btn.H, btn.H/2, 1, glassAccent())
	drawText(renderer, font, label, btn.X+16, btn.Y+(btn.H-bh)/2, ColorTextPrimary(), textAlignLeft)
	if small := getFontSized(config, "small", 13); small != nil {
		yw, yh := measureText(small, "Y")
		drawText(renderer, small, "Y", btn.X+btn.W-18-yw, btn.Y+(btn.H-yh)/2, ColorTextTertiary(), textAlignLeft)
	}
	text := ellipsize(font, mediaFavUndo.label, btn.X-bar.X-36)
	_, th := measureText(font, text)
	drawText(renderer, font, text, bar.X+18, bar.Y+(bar.H-th)/2, ColorTextSecondary(), textAlignLeft)
	// The bar swallows taps that miss its button, so a card under it does
	// not start playing.
	mediaHub.hits = append(mediaHub.hits,
		mediaHit{kind: mediaHitStateAction, a: -1, rect: bar},
		mediaHit{kind: mediaHitUndo, rect: btn})
}
