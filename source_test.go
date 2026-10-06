package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSourceServer(t *testing.T) (*server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "a.d2")
	if err := os.WriteFile(file, []byte("a -> b\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := newServer("elk", 0)
	s.register(file)
	go s.watchLoop()
	mux := http.NewServeMux()
	mux.HandleFunc("/source", s.handleSource)
	mux.HandleFunc("/events", s.handleEvents)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts, file
}

func putSource(t *testing.T, ts *httptest.Server, file, base, text string) (*http.Response, map[string]string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"file": file, "baseHash": base, "text": text})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/source", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestPutSource(t *testing.T) {
	_, ts, file := newSourceServer(t)
	base := contentHash([]byte("a -> b\n"))

	resp, out := putSource(t, ts, file, base, "a -> c\n")
	if resp.StatusCode != http.StatusOK || out["hash"] != contentHash([]byte("a -> c\n")) {
		t.Fatalf("put: %d %v", resp.StatusCode, out)
	}
	got, _ := os.ReadFile(file)
	fi, _ := os.Stat(file)
	if string(got) != "a -> c\n" || fi.Mode().Perm() != 0o640 {
		t.Fatalf("file = %q mode %v", got, fi.Mode())
	}

	resp, out = putSource(t, ts, file, base, "x\n")
	if resp.StatusCode != http.StatusConflict || out["text"] != "a -> c\n" {
		t.Fatalf("stale put: %d %v", resp.StatusCode, out)
	}

	resp, _ = putSource(t, ts, filepath.Join(filepath.Dir(file), "other.d2"), base, "x\n")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unregistered put: %d", resp.StatusCode)
	}
}

func TestSourceEvent(t *testing.T) {
	_, ts, file := newSourceServer(t)
	resp, err := http.Get(ts.URL + "/events?file=" + url.QueryEscape(file))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			events <- sc.Text()
		}
	}()
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(file, []byte("a -> z\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-events:
			payload, ok := strings.CutPrefix(line, "data: {")
			if !ok {
				continue
			}
			var ev struct{ Text string }
			if json.Unmarshal([]byte("{"+payload), &ev) == nil && ev.Text == "a -> z\n" {
				return
			}
		case <-deadline:
			t.Fatal("no source event after an external write")
		}
	}
}
