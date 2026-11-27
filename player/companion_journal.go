package main

// companion_journal.go — the Games card's Journal fields on the phone: a
// game's status, rating and note, read and written from the same records the
// handheld's game page uses. There is no second store, so a note typed on the
// phone is on the handheld's game page at once.
//
// GET  /api/games/journal?id=<game> answers one game, or every game that has
//      a status, a rating or a note when id is absent (bounded).
// POST /api/games/journal takes {"id","status","rating","note"} and answers
//      the game as it is now. A status is one of the five built-in ones, the
//      rating is 0–5, and a note is at most gmetaMaxText characters.
//
// Both routes are behind the PIN like every other Games call. Only a game in
// the phone's copy of the library can be changed; a made-up id gets a 404.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const companionJournalBodyMax = 16 << 10

var (
	errJournalStatus = errors.New("Unknown status")
	errJournalRating = errors.New("Rating must be 0 to 5")
	errJournalNote   = errors.New("Note is too long")
	errJournalID     = errors.New("Unknown game")
)

// companionJournalItem is one game's journal as the phone sees it.
type companionJournalItem struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Console string `json:"console,omitempty"`
	Status  string `json:"status,omitempty"`
	Rating  int    `json:"rating,omitempty"`
	Note    string `json:"note,omitempty"`
}

// companionJournalReadOne copies a game's record into a phone item.
func companionJournalReadOne(game companionGame) companionJournalItem {
	item := companionJournalItem{ID: game.ID, Title: game.Title, Console: game.Console}
	if rec, ok := gmetaGet(game.game.System, game.game.Title); ok {
		item.Status = gmetaStatus(rec)
		item.Rating = rec.Rating
		item.Note = rec.Notes
	}
	return item
}

// handleGamesJournal answers GET and POST /api/games/journal.
func (s *companionServer) handleGamesJournal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" {
			game, ok := companionLibGame(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeCompanionJSON(w, map[string]interface{}{
				"game": companionJournalReadOne(game), "statuses": gmetaStatusTags,
			})
			return
		}
		games, _, _ := companionLibSnapshot()
		items := make([]companionJournalItem, 0, len(games))
		for _, game := range games {
			item := companionJournalReadOne(game)
			if item.Status != "" || item.Rating > 0 || strings.TrimSpace(item.Note) != "" {
				items = append(items, item)
			}
			if len(items) >= 200 {
				break
			}
		}
		writeCompanionJSON(w, map[string]interface{}{"journal": items, "statuses": gmetaStatusTags})
	case http.MethodPost:
		s.handleGamesJournalWrite(w, r)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// handleGamesJournalWrite stores the phone's changes in the game's record.
func (s *companionServer) handleGamesJournalWrite(w http.ResponseWriter, r *http.Request) {
	var call companionJournalItem
	body, err := io.ReadAll(io.LimitReader(r.Body, companionJournalBodyMax+1))
	if err != nil || len(body) > companionJournalBodyMax {
		http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := json.Unmarshal(body, &call); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	game, ok := companionLibGame(strings.TrimSpace(call.ID))
	if !ok {
		http.Error(w, errJournalID.Error(), http.StatusNotFound)
		return
	}
	if call.Status != "" && !gmetaIsStatusTag(gmetaCleanTag(call.Status)) {
		http.Error(w, errJournalStatus.Error(), http.StatusBadRequest)
		return
	}
	if call.Rating < 0 || call.Rating > 5 {
		http.Error(w, errJournalRating.Error(), http.StatusBadRequest)
		return
	}
	if len([]rune(call.Note)) > gmetaMaxText {
		http.Error(w, errJournalNote.Error(), http.StatusBadRequest)
		return
	}
	gmetaSetStatus(game.game.System, game.game.Title, call.Status)
	rec := gmetaUpdate(game.game.System, game.game.Title, func(rec *gmetaRecord) {
		rec.Rating = call.Rating
		rec.Notes = strings.TrimSpace(call.Note)
	})
	item := companionJournalReadOne(game)
	item.Status, item.Rating, item.Note = gmetaStatus(rec), rec.Rating, rec.Notes
	writeCompanionJSON(w, map[string]interface{}{"game": item})
}
