package drive

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testFolderID = "folder-1"

// stub stands in for the Drive API, serving a fixed set of files.
type stub struct {
	files map[string]string // id -> contents
	names map[string]string // id -> name
	// folderStatus lets a test simulate a folder that was never shared.
	folderStatus int
	// pageSize forces paging when set.
	pageSize                 int
	listCalls, downloadCalls int
}

func (s *stub) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/files/")

		if id == testFolderID {
			if s.folderStatus != 0 && s.folderStatus != http.StatusOK {
				w.WriteHeader(s.folderStatus)
				w.Write([]byte(`{"error":{"message":"File not found"}}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]string{
				"id": id, "name": "gpx", "mimeType": "application/vnd.google-apps.folder",
			})
			return
		}

		body, ok := s.files[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("alt") != "media" {
			t.Errorf("download of %s did not ask for media", id)
		}
		s.downloadCalls++
		w.Write([]byte(body))
	})

	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		s.listCalls++

		if q := r.URL.Query().Get("q"); !strings.Contains(q, testFolderID) {
			t.Errorf("listing query %q does not restrict to the folder", q)
		}
		if r.URL.Query().Get("supportsAllDrives") != "true" {
			t.Error("listing did not set supportsAllDrives, so a shared drive would be invisible")
		}

		ids := make([]string, 0, len(s.names))
		for id := range s.names {
			ids = append(ids, id)
		}
		// Deterministic order so paging is testable.
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				if s.names[ids[j]] < s.names[ids[i]] {
					ids[i], ids[j] = ids[j], ids[i]
				}
			}
		}

		start := 0
		if tok := r.URL.Query().Get("pageToken"); tok != "" {
			fmt.Sscanf(tok, "%d", &start)
		}
		end := len(ids)
		next := ""
		if s.pageSize > 0 && start+s.pageSize < len(ids) {
			end = start + s.pageSize
			next = fmt.Sprint(end)
		}

		out := struct {
			NextPageToken string `json:"nextPageToken,omitempty"`
			Files         []file `json:"files"`
		}{NextPageToken: next}
		for _, id := range ids[start:end] {
			out.Files = append(out.Files, file{ID: id, Name: s.names[id]})
		}
		json.NewEncoder(w).Encode(out)
	})

	return mux
}

func newTestClient(t *testing.T, s *stub) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)

	dest := t.TempDir()
	return &Client{
		folderID: testFolderID,
		dest:     dest,
		email:    "tracker@example.iam.gserviceaccount.com",
		http:     srv.Client(),
		timeout:  10 * time.Second,
		base:     srv.URL,
	}, dest
}

func TestPull_DownloadsRecordings(t *testing.T) {
	s := &stub{
		files: map[string]string{"a": "<gpx>one</gpx>", "b": "<gpx>two</gpx>"},
		names: map[string]string{"a": "20260910130000.gpx", "b": "20260910183000.gpx"},
	}
	c, dest := newTestClient(t, s)

	if err := c.Pull(t.Context()); err != nil {
		t.Fatalf("Pull returned %v", err)
	}

	for id, name := range s.names {
		body, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(body) != s.files[id] {
			t.Errorf("%s contains %q, want %q", name, body, s.files[id])
		}
	}
}

func TestPull_SkipsFilesAlreadyPresent(t *testing.T) {
	s := &stub{
		files: map[string]string{"a": "<gpx>new</gpx>"},
		names: map[string]string{"a": "20260910130000.gpx"},
	}
	c, dest := newTestClient(t, s)

	// A file already in the inbox must be left exactly as it is, so that a
	// repeated pull transfers nothing and cannot overwrite local state.
	existing := filepath.Join(dest, "20260910130000.gpx")
	if err := os.WriteFile(existing, []byte("<gpx>original</gpx>"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := c.Pull(t.Context()); err != nil {
		t.Fatalf("Pull returned %v", err)
	}
	if s.downloadCalls != 0 {
		t.Errorf("downloaded %d files, want 0", s.downloadCalls)
	}
	body, _ := os.ReadFile(existing)
	if string(body) != "<gpx>original</gpx>" {
		t.Errorf("existing file was overwritten: %q", body)
	}
}

func TestPull_IgnoresNonRecordings(t *testing.T) {
	s := &stub{
		files: map[string]string{"a": "<gpx/>", "b": "notes", "c": "photo"},
		names: map[string]string{"a": "20260910130000.gpx", "b": "readme.txt", "c": "snap.jpg"},
	}
	c, dest := newTestClient(t, s)

	if err := c.Pull(t.Context()); err != nil {
		t.Fatalf("Pull returned %v", err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 1 || entries[0].Name() != "20260910130000.gpx" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("inbox holds %v, want only the recording", names)
	}
}

// TestPull_RefusesNamesThatAreNotPlainFiles guards the one place a remote value
// reaches the filesystem. The name is also the key the tracker deduplicates on.
func TestPull_RefusesNamesThatAreNotPlainFiles(t *testing.T) {
	s := &stub{
		files: map[string]string{"a": "x", "b": "y"},
		names: map[string]string{"a": "../escape.gpx", "b": "ok.gpx"},
	}
	c, dest := newTestClient(t, s)

	if err := c.Pull(t.Context()); err != nil {
		t.Fatalf("Pull returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "..", "escape.gpx")); err == nil {
		t.Error("a file escaped the inbox directory")
	}
	if _, err := os.Stat(filepath.Join(dest, "ok.gpx")); err != nil {
		t.Error("the well-named file was not downloaded")
	}
}

func TestPull_UnsharedFolderSaysWhatToDo(t *testing.T) {
	s := &stub{folderStatus: http.StatusNotFound}
	c, _ := newTestClient(t, s)

	err := c.Pull(t.Context())
	if err == nil {
		t.Fatal("Pull succeeded against a folder it cannot read")
	}
	// The mistake that actually happens is forgetting to share the folder, so
	// the message has to name the address to share it with.
	if !strings.Contains(err.Error(), c.email) {
		t.Errorf("error %q does not name the service account", err)
	}
	if !strings.Contains(err.Error(), "Viewer") {
		t.Errorf("error %q does not say what access to grant", err)
	}
	if s.listCalls != 0 {
		t.Error("the folder listing was attempted even though the folder is unreadable")
	}
}

func TestPull_FollowsPaging(t *testing.T) {
	s := &stub{files: map[string]string{}, names: map[string]string{}, pageSize: 2}
	for i := range 5 {
		id := fmt.Sprintf("f%d", i)
		s.files[id] = fmt.Sprintf("<gpx>%d</gpx>", i)
		s.names[id] = fmt.Sprintf("2026091013000%d.gpx", i)
	}
	c, dest := newTestClient(t, s)

	if err := c.Pull(t.Context()); err != nil {
		t.Fatalf("Pull returned %v", err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 5 {
		t.Errorf("downloaded %d recordings, want 5", len(entries))
	}
	if s.listCalls < 3 {
		t.Errorf("made %d listing calls, want at least 3 for a paged folder", s.listCalls)
	}
}

func TestPull_LeavesNoPartialFiles(t *testing.T) {
	s := &stub{
		files: map[string]string{"a": "<gpx/>"},
		names: map[string]string{"a": "20260910130000.gpx"},
	}
	c, dest := newTestClient(t, s)
	if err := c.Pull(t.Context()); err != nil {
		t.Fatalf("Pull returned %v", err)
	}

	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".download-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestNew_NotConfigured(t *testing.T) {
	tests := []struct {
		name     string
		creds    Credentials
		folderID string
	}{
		{name: "no folder", creds: Credentials{Base64: "e30="}, folderID: ""},
		{name: "no credentials", folderID: "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(t.Context(), tt.creds, tt.folderID, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "no folder configured") {
				t.Errorf("New returned %v, want ErrNotConfigured", err)
			}
		})
	}
}

// TestNew_RejectsKeyMissingItsFields covers a key that parses but cannot sign.
// Accepting one defers the failure to the first refresh and produces a message
// asking the folder to be shared with nobody.
func TestNew_RejectsKeyMissingItsFields(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(`{"type":"service_account"}`))

	_, err := New(t.Context(), Credentials{Base64: key}, "folder", t.TempDir())
	if err == nil {
		t.Fatal("New accepted a key with no address and no private key")
	}
	if !strings.Contains(err.Error(), "client_email") {
		t.Errorf("error %q does not say what the key is missing", err)
	}
}

func TestCredentials_Resolve(t *testing.T) {
	want := `{"type":"service_account"}`

	got, err := Credentials{Base64: base64.StdEncoding.EncodeToString([]byte(want))}.resolve()
	if err != nil || string(got) != want {
		t.Errorf("from base64 = %q, %v", got, err)
	}

	path := filepath.Join(t.TempDir(), "key.json")
	os.WriteFile(path, []byte(want), 0o600)
	got, err = Credentials{Path: path}.resolve()
	if err != nil || string(got) != want {
		t.Errorf("from file = %q, %v", got, err)
	}

	if _, err := (Credentials{Base64: "not base64!"}).resolve(); err == nil {
		t.Error("malformed base64 was accepted")
	}
}
