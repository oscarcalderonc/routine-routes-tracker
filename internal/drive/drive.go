// Package drive copies newly recorded files from a Google Drive folder into a
// local directory.
//
// Access is by service account: the folder is shared with the account's address
// the same way it would be shared with a person, and the account's key is the
// only credential. That avoids an interactive consent step and a refresh token
// to store, which matters for something that runs unattended on a home server.
//
// Only the token exchange is delegated, to golang.org/x/oauth2, because signing
// assertions is not worth implementing by hand. The two Drive calls needed are
// plain HTTP requests, so the full API client and the dependency tree that comes
// with it are not pulled in.
package drive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

const (
	// readonlyScope is the narrowest scope that can read a shared folder.
	readonlyScope = "https://www.googleapis.com/auth/drive.readonly"
	apiBase       = "https://www.googleapis.com/drive/v3"
	// maxPages bounds paging so a misbehaving response cannot loop forever.
	maxPages = 50
)

// ErrNotConfigured reports that no Drive folder has been set up. It is not a
// failure: recordings placed in the inbox by any other means are still imported.
var ErrNotConfigured = errors.New("drive: no folder configured")

// Client reads recordings from one Drive folder.
type Client struct {
	folderID string
	dest     string
	email    string
	http     *http.Client
	timeout  time.Duration
	// base is the API root, overridden by tests.
	base string
}

// Credentials holds a service account key, supplied either already decoded or
// as the base64 of the key file.
type Credentials struct {
	// JSON is the key file's contents.
	JSON []byte
	// Base64 is the key file encoded, as it is more convenient to carry in an
	// environment variable.
	Base64 string
	// Path is a file holding the key.
	Path string
}

// resolve returns the key file's contents from whichever form was supplied.
func (c Credentials) resolve() ([]byte, error) {
	switch {
	case len(c.JSON) > 0:
		return c.JSON, nil
	case c.Base64 != "":
		body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.Base64))
		if err != nil {
			return nil, fmt.Errorf("decode service account key: %w", err)
		}
		return body, nil
	case c.Path != "":
		body, err := os.ReadFile(c.Path)
		if err != nil {
			return nil, fmt.Errorf("read service account key: %w", err)
		}
		return body, nil
	}
	return nil, ErrNotConfigured
}

// New returns a client for the given folder. It reports ErrNotConfigured when no
// credentials or no folder have been supplied, which the caller should treat as
// "no cloud folder in use" rather than as an error.
func New(ctx context.Context, creds Credentials, folderID, dest string) (*Client, error) {
	if folderID == "" {
		return nil, ErrNotConfigured
	}
	key, err := creds.resolve()
	if err != nil {
		return nil, err
	}

	conf, err := google.JWTConfigFromJSON(key, readonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parse service account key: %w", err)
	}
	// A key missing these parses without complaint but cannot sign anything, and
	// the failure would otherwise surface only at the first refresh, in a
	// message naming an empty address to share the folder with.
	if conf.Email == "" || len(conf.PrivateKey) == 0 {
		return nil, errors.New("drive: service account key has no client_email or private_key")
	}

	return &Client{
		folderID: folderID,
		dest:     dest,
		// Carried only so that a permission failure can name the address the
		// folder has to be shared with, which is the mistake that actually
		// happens.
		email:   conf.Email,
		http:    conf.Client(ctx),
		timeout: 5 * time.Minute,
		base:    apiBase,
	}, nil
}

// Account returns the address the folder must be shared with.
func (c *Client) Account() string { return c.email }

// Configured reports that a folder is in use.
func (c *Client) Configured() bool { return c != nil && c.folderID != "" }

// Pull downloads any recording in the folder that is not already in the local
// directory. Existing files are left alone, so a repeated pull transfers only
// what is new.
func (c *Client) Pull(ctx context.Context) error {
	if !c.Configured() {
		return nil
	}
	if err := os.MkdirAll(c.dest, 0o755); err != nil {
		return fmt.Errorf("create inbox: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Checking the folder first turns the common setup mistake into a message
	// that says what to do, rather than an empty listing that looks like an
	// empty folder.
	if err := c.checkFolder(ctx); err != nil {
		return err
	}

	files, err := c.list(ctx)
	if err != nil {
		return err
	}

	for _, f := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !strings.EqualFold(filepath.Ext(f.Name), ".gpx") {
			continue
		}
		// The local name is the key everything downstream uses, so anything
		// that is not a plain file name is refused rather than interpreted.
		if f.Name != filepath.Base(f.Name) || f.Name == "" {
			continue
		}
		path := filepath.Join(c.dest, f.Name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := c.download(ctx, f, path); err != nil {
			return err
		}
	}
	return nil
}

// file is the subset of a Drive file's metadata that matters here.
type file struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) checkFolder(ctx context.Context) error {
	q := url.Values{
		"fields":            {"id,name,mimeType"},
		"supportsAllDrives": {"true"},
	}
	req, err := c.request(ctx, c.base+"/files/"+url.PathEscape(c.folderID)+"?"+q.Encode())
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach Google Drive: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound, http.StatusForbidden:
		return fmt.Errorf("drive folder %s is not readable by %s; "+
			"share the folder with that address, giving it at least Viewer access",
			c.folderID, c.email)
	default:
		return fmt.Errorf("check drive folder: %s: %s", resp.Status, readError(resp.Body))
	}
}

func (c *Client) list(ctx context.Context) ([]file, error) {
	var out []file
	var pageToken string

	for page := 0; page < maxPages; page++ {
		q := url.Values{
			// Restricting to the folder's direct children keeps anything else
			// in the account out of reach of this program.
			"q":                         {fmt.Sprintf("%q in parents and trashed = false", c.folderID)},
			"fields":                    {"nextPageToken,files(id,name)"},
			"pageSize":                  {"1000"},
			"orderBy":                   {"name"},
			"supportsAllDrives":         {"true"},
			"includeItemsFromAllDrives": {"true"},
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}

		req, err := c.request(ctx, c.base+"/files?"+q.Encode())
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list drive folder: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			err := fmt.Errorf("list drive folder: %s: %s", resp.Status, readError(resp.Body))
			resp.Body.Close()
			return nil, err
		}

		var body struct {
			NextPageToken string `json:"nextPageToken"`
			Files         []file `json:"files"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode drive listing: %w", err)
		}

		out = append(out, body.Files...)
		if body.NextPageToken == "" {
			return out, nil
		}
		pageToken = body.NextPageToken
	}
	return out, fmt.Errorf("drive folder has more than %d pages of files", maxPages)
}

// download writes one file, via a temporary name so that an interrupted transfer
// cannot leave a truncated recording that would then be treated as seen.
func (c *Client) download(ctx context.Context, f file, path string) error {
	q := url.Values{"alt": {"media"}, "supportsAllDrives": {"true"}}
	req, err := c.request(ctx, c.base+"/files/"+url.PathEscape(f.ID)+"?"+q.Encode())
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", f.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s: %s", f.Name, resp.Status, readError(resp.Body))
	}

	tmp, err := os.CreateTemp(c.dest, ".download-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", f.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", f.Name, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("commit %s: %w", f.Name, err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build drive request: %w", err)
	}
	return req, nil
}

// readError returns a short, bounded excerpt of an error response for logging.
func readError(r io.Reader) string {
	body, err := io.ReadAll(io.LimitReader(r, 512))
	if err != nil {
		return "unreadable response"
	}
	return strings.TrimSpace(string(body))
}
