package graph

// Attachment upload/download HTTP handlers. Lives next to graph
// because the upload response JSON-roundtrips into AttachmentInput
// (defined in models_gen.go) — same wire shape, same fields, no
// translation needed. Multipart and streaming downloads don't fit
// GraphQL's model cleanly, so they ride native HTTP; co-locating
// with graph keeps the attachment-related types in one place.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/elijahmontenegro/grudge/service/storage"

	"github.com/google/uuid"
)

// MaxAttachmentBytes caps any single uploaded file. 100MB covers
// reasonable docs/screenshots/PDFs without letting an accidental
// drag-and-drop of a database dump wedge the service.
const MaxAttachmentBytes = 100 * 1024 * 1024

// InlinedTextCap is the maximum amount of extracted text RRC will
// chunk and score from a text-like attachment. Attachments past
// this are still readable by the agent via FileRead; only the
// excerpt that RRC scores is capped, so a 50MB log file doesn't
// produce hundreds of chunks nobody wants scored.
const InlinedTextCap = 64 * 1024

// fileIDRe validates UUIDs we return from upload before using them
// in paths on download. Defense in depth — clients construct the
// full URL themselves, so every path segment gets checked.
var fileIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AttachmentManager owns the filesystem layout and path validation
// for upload/download endpoints.
type AttachmentManager struct {
	dataDir string
}

// NewAttachmentManager binds the manager to the grudge data directory.
// All attachment paths resolve under
// {dataDir}/sandboxes/sbx-{threadID}/_attachments/ — same root the
// sandbox mounts at /workspace, so attachments are immediately visible
// to the agent without extra mount config.
func NewAttachmentManager(dataDir string) *AttachmentManager {
	return &AttachmentManager{dataDir: dataDir}
}

// HandleUpload accepts POST /api/attachments/{threadID} as
// multipart/form-data with one or more files under the "files" field.
// Writes each file to {workspace}/_attachments/{uuid}/{sanitized-name},
// sniffs MIME from the actual content (not the client's claim),
// extracts text for text/* MIME up to InlinedTextCap, and returns a
// JSON array of api.Attachment — JSON shape matches the gqlgen
// graph.AttachmentInput so the client round-trips the response —
// echoes back into sendMessage/startAutonomous. One schema (gqlgen),
// one Go type, no parallel DTO.
func (m *AttachmentManager) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	threadID := strings.TrimPrefix(r.URL.Path, "/api/attachments/")
	threadID = strings.Trim(threadID, "/")
	if err := storage.ValidateThreadID(threadID); err != nil {
		http.Error(w, "invalid thread id", http.StatusBadRequest)
		return
	}

	// 32MB in-memory form limit is a multipart-parse limit only — large
	// file bodies overflow to temp files, then we stream them through a
	// size-limited copy into the workspace.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, fmt.Sprintf("parse multipart: %v", err), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "no files provided", http.StatusBadRequest)
		return
	}

	attachRoot := filepath.Join(m.dataDir, "sandboxes", "sbx-"+threadID, "_attachments")
	if err := os.MkdirAll(attachRoot, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("mkdir: %v", err), http.StatusInternalServerError)
		return
	}

	out := make([]*AttachmentInput, 0, len(files))
	for _, fh := range files {
		if fh.Size > MaxAttachmentBytes {
			http.Error(w, fmt.Sprintf("file too large: %s (%d bytes, cap %d)",
				fh.Filename, fh.Size, MaxAttachmentBytes), http.StatusRequestEntityTooLarge)
			return
		}
		meta, err := m.saveHeader(fh, attachRoot)
		if err != nil {
			http.Error(w, fmt.Sprintf("save %s: %v", fh.Filename, err), http.StatusInternalServerError)
			return
		}
		out = append(out, meta)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// saveHeader streams one multipart file to disk and returns its
// metadata. Generates a fresh UUID for the subdirectory so two
// attachments with the same display name coexist.
func (m *AttachmentManager)saveHeader(fh *multipart.FileHeader, attachRoot string) (*AttachmentInput, error) {
	id := uuid.NewString()
	clean := sanitizeFilename(fh.Filename)
	if clean == "" {
		clean = "untitled"
	}

	subdir := filepath.Join(attachRoot, id)
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		return nil, err
	}
	dstPath := filepath.Join(subdir, clean)

	src, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	// Stream with a hard ceiling — LimitReader + trailing read check
	// detects over-size files that claimed a lower Size in the header.
	limited := io.LimitReader(src, MaxAttachmentBytes+1)
	written, copyErr := io.Copy(dst, limited)
	if cerr := dst.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return nil, copyErr
	}
	if written > MaxAttachmentBytes {
		_ = os.Remove(dstPath)
		return nil, fmt.Errorf("file exceeded size ceiling")
	}

	// Sniff MIME from the head of the actual file — don't trust the
	// client's Content-Type. For text MIME extract an excerpt for RRC.
	mimeType, inlined := sniffAndExtract(dstPath)

	// Sandbox-relative path — inside the container this is
	// /workspace/_attachments/{id}/{filename}. Workspace root is
	// always /workspace, regardless of host path, per sandbox mount.
	sandboxPath := path.Join("/workspace", "_attachments", id, clean)

	// InlinedText is *string so an empty excerpt serializes as omitted
	// JSON rather than as "" — same shape gqlgen's AttachmentInput
	// expects when the client echoes this back into the GraphQL input.
	var inlinedPtr *string
	if inlined != "" {
		inlinedPtr = &inlined
	}

	return &AttachmentInput{
		ID:          id,
		Filename:    clean,
		MimeType:    mimeType,
		SizeBytes:   int(written),
		Path:        sandboxPath,
		InlinedText: inlinedPtr,
	}, nil
}

// HandleDownload streams an attachment back to the client.
// GET /api/attachments/{threadID}/{fileID}/{filename}
func (m *AttachmentManager) HandleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/attachments/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	threadID, fileID, filename := parts[0], parts[1], parts[2]
	if err := storage.ValidateThreadID(threadID); err != nil {
		http.Error(w, "invalid thread id", http.StatusBadRequest)
		return
	}
	if !fileIDRe.MatchString(fileID) {
		http.Error(w, "invalid file id", http.StatusBadRequest)
		return
	}
	clean := sanitizeFilename(filename)
	if clean == "" || clean != filename {
		// If sanitization changed the filename, the client is
		// either malformed or trying something. Reject rather than
		// quietly serving a different file.
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(m.dataDir, "sandboxes", "sbx-"+threadID, "_attachments", fileID, clean)
	// Re-resolve and containment-check — belt and suspenders against
	// any traversal that slipped past sanitization.
	abs, err := filepath.Abs(fullPath)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	root, err := filepath.Abs(filepath.Join(m.dataDir, "sandboxes", "sbx-"+threadID, "_attachments"))
	if err != nil || !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		http.Error(w, "out of bounds", http.StatusBadRequest)
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}

	// Re-sniff MIME on download so a later manual file swap on disk
	// doesn't advertise a stale type. Cheap (512 byte read).
	mimeType, _ := sniffAndExtract(abs)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, clean))
	if _, err := io.Copy(w, f); err != nil {
		// Client disconnected mid-stream or write failed — nothing
		// to do but stop. Headers are already sent.
		return
	}
}

// sanitizeFilename strips path separators, traversal fragments, null
// bytes, and leading/trailing whitespace. Windows-reserved basenames
// (CON, NUL, etc.) get suffixed so they can't clash with devices.
func sanitizeFilename(name string) string {
	// Take just the last path component — caller may have sent
	// something like "folder/file.pdf" or "..\\..\\win.ini".
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "/", "")
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.TrimSpace(name)
	// No leading dots — avoids hidden-file tricks and "..." paths.
	for strings.HasPrefix(name, ".") {
		name = strings.TrimPrefix(name, ".")
	}
	if name == "" {
		return ""
	}
	// Windows reserved names.
	upper := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch upper {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	if len(name) > 255 {
		// Keep extension if present.
		ext := filepath.Ext(name)
		keep := 255 - len(ext)
		if keep < 1 {
			keep = 1
		}
		name = name[:keep] + ext
	}
	return name
}

// sniffAndExtract reads the first bytes of a file to determine its
// MIME type, and extracts text content up to InlinedTextCap if the
// file is text-like. Returns ("", "") on read failure — caller treats
// unknown MIME as application/octet-stream.
func sniffAndExtract(filePath string) (mimeType, inlined string) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]
	mimeType = http.DetectContentType(head)
	// DetectContentType returns "text/plain; charset=utf-8" style —
	// normalize for our comparisons while keeping the charset bit for
	// the Content-Type header later.
	mediaType, _, _ := mime.ParseMediaType(mimeType)
	if mediaType == "" {
		mediaType = mimeType
	}

	// Also recognize common text-like MIMEs whose detector output
	// isn't exactly "text/*" — e.g. JSON, JS, CSS.
	isText := strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" ||
		mediaType == "application/xml" ||
		mediaType == "application/javascript" ||
		mediaType == "application/x-yaml"

	// Refine by extension — DetectContentType is generic, e.g.
	// markdown reads as text/plain; we want to record that as
	// text/markdown so the frontend can render appropriately.
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".md", ".markdown":
		mimeType = "text/markdown; charset=utf-8"
		isText = true
	case ".yaml", ".yml":
		mimeType = "application/x-yaml"
		isText = true
	case ".json":
		mimeType = "application/json"
		isText = true
	case ".csv":
		mimeType = "text/csv; charset=utf-8"
		isText = true
	}

	if !isText {
		return mimeType, ""
	}

	// Read back from start up to the cap.
	if _, err := f.Seek(0, 0); err != nil {
		return mimeType, ""
	}
	buf := make([]byte, InlinedTextCap)
	read, _ := io.ReadFull(f, buf)
	excerpt := string(buf[:read])
	// Drop any invalid UTF-8 — RRC text path assumes valid strings.
	if !utf8.ValidString(excerpt) {
		excerpt = strings.ToValidUTF8(excerpt, "")
	}
	return mimeType, excerpt
}
