package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/hiylo/opencode-backend/internal/store"
)

// handleTasks lists (GET) and creates (POST) orchestration tasks.
// Both require a valid APP token.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireToken(r); !ok {
		writeErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listTasks(w, r)
	case http.MethodPost:
		s.createTask(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTasksStatus lists tasks filtered by ?status=.
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tasks, err := s.store.ListTasks(ctx, status, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list tasks failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// createTask accepts {"prompt","sessionId"?,"directory"?} and enqueues it.
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt    string `json:"prompt"`
		SessionID string `json:"sessionId"`
		Directory string `json:"directory"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "prompt is required")
		return
	}
	t := &store.Task{
		ID:        newTaskID(),
		SessionID: req.SessionID,
		Directory: req.Directory,
		Prompt:    req.Prompt,
	}
	if err := s.store.CreateTask(r.Context(), t); err != nil {
		writeErr(w, http.StatusInternalServerError, "create task failed")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// handleTaskByID reads (GET) or cancels (DELETE) a single task.
func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireToken(r); !ok {
		writeErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	id := r.URL.Path[len("/api/tasks/"):]
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing task id")
		return
	}
	switch r.Method {
	case http.MethodGet:
		t, err := s.store.GetTask(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "task not found")
			return
		}
		writeJSON(w, http.StatusOK, t)
	case http.MethodDelete:
		canceled, err := s.store.CancelTask(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "cancel failed")
			return
		}
		if !canceled {
			writeErr(w, http.StatusConflict, "task already finished")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func newTaskID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("task_%s", hex.EncodeToString(buf))
}