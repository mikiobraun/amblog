package amagg

import (
	"errors"
	"net/http"
	"strconv"

	"atomicmicro.blog/internal/web"
)

// Feed reader credentials: plain feed readers can't run the login flow or
// send bearer tokens, but they do HTTP Basic. A member creates one password
// per reader; it works only on feeds.

func (s *Server) handleCreateReader(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Name string `json:"name"`
	}
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		web.WriteError(w, http.StatusBadRequest, "name is required, e.g. the reader app's name")
		return
	}
	password, hash := web.NewToken("amr_")
	reader, err := s.store.CreateReader(u.ID, req.Name, hash)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":           reader.ID,
		"name":         reader.Name,
		"created":      reader.Created,
		"username":     u.Name,
		"password":     password, // shown only once
		"timeline_url": s.timelineURL(u),
	})
}

func (s *Server) handleListReaders(w http.ResponseWriter, r *http.Request, u *User) {
	readers, err := s.store.Readers(u.ID)
	if err != nil {
		web.ServerError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"readers": readers})
}

func (s *Server) handleDeleteReader(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	err = s.store.DeleteReader(u.ID, id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		web.ServerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
