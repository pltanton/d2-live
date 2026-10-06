package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// writeAtomic replaces path in one rename so the watcher and readers never see a
// half-written diagram.
func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// sourceChanged reports whether abs's content hash differs from the one tabs
// were last told about, and records it.
func (s *server) sourceChanged(abs string, hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[abs] == hash {
		return false
	}
	s.seen[abs] = hash
	return true
}

func (s *server) isRegistered(abs string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[abs]
	return ok
}

func (s *server) registeredFile(w http.ResponseWriter, file string) (string, bool) {
	if file == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return "", false
	}
	abs, err := filepath.Abs(file)
	if err != nil || !s.isRegistered(abs) {
		http.Error(w, "file is not open in d2-live", http.StatusForbidden)
		return "", false
	}
	return abs, true
}

func (s *server) handleSource(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		abs, ok := s.registeredFile(w, r.URL.Query().Get("file"))
		if !ok {
			return
		}
		text, err := readText(abs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text, "hash": contentHash([]byte(text))})
	case http.MethodPut:
		var req struct {
			File     string `json:"file"`
			BaseHash string `json:"baseHash"`
			Text     string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		abs, ok := s.registeredFile(w, req.File)
		if !ok {
			return
		}
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		current, err := readText(abs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cur := contentHash([]byte(current)); cur != req.BaseHash {
			writeJSON(w, http.StatusConflict, map[string]string{"reason": "stale", "text": current, "hash": cur})
			return
		}
		hash := contentHash([]byte(req.Text))
		if err := writeAtomic(abs, []byte(req.Text)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Tabs re-fetch now instead of after the watcher's debounce; the render
		// cache is keyed by content hash, so the watcher's later reload is a hit.
		s.notify(abs, "reload")
		writeJSON(w, http.StatusOK, map[string]string{"hash": hash})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
