// Package workspace owns governed file tools and durable edit evidence. It is
// a Focus product extension, not part of the generic Agent or Forest runtime.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Leeminjing/Focus-Go/internal/domain"
)

var ErrDenied = errors.New("workspace access denied")
var ErrVersion = errors.New("workspace file version conflict")
var ErrUnknown = errors.New("workspace edit outcome unknown; verify durable evidence")

const maxFile = 1 << 20

type Stage string

const (
	Prepared     Stage = "prepared"
	FileReplaced Stage = "file-replaced"
	ReceiptSaved Stage = "receipt-saved"
)

type Workspace struct {
	binding   domain.Workspace
	root      *os.Root
	directory string
	mu        sync.Mutex
	observer  func(Stage, domain.Receipt) error
}

func Bind(id, root string, read, write []string, preapproved bool) (domain.Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.Workspace{}, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return domain.Workspace{}, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return domain.Workspace{}, ErrDenied
	}
	identity, _, err := identity(f, info)
	if err != nil {
		return domain.Workspace{}, err
	}
	binding := domain.Workspace{ID: id, Root: abs, Identity: identity, Read: append([]string{}, read...), Write: append([]string{}, write...), Preapproved: preapproved}
	sort.Strings(binding.Read)
	sort.Strings(binding.Write)
	if err := validateBinding(binding); err != nil {
		return binding, err
	}
	return binding, nil
}
func validateBinding(b domain.Workspace) error {
	if (runtime.GOOS != "windows" && runtime.GOOS != "linux") || b.Validate() != nil || !filepath.IsAbs(b.Root) {
		return ErrDenied
	}
	for _, rules := range [][]string{b.Read, b.Write} {
		seen := map[string]bool{}
		for _, rule := range rules {
			if seen[strings.ToLower(rule)] {
				return ErrDenied
			}
			seen[strings.ToLower(rule)] = true
			if rule != "." && !validPath(strings.TrimSuffix(rule, "/")) {
				return ErrDenied
			}
		}
	}
	for _, rule := range b.Write {
		if !allowed(b.Read, rule) {
			return ErrDenied
		}
	}
	return nil
}
func New(binding domain.Workspace, directory string, observer func(Stage, domain.Receipt) error) (*Workspace, error) {
	if err := validateBinding(binding); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(binding.Root, abs)
	if err != nil || rel == "." || filepath.IsLocal(rel) {
		return nil, ErrDenied
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	rel, err = filepath.Rel(binding.Root, abs)
	if err != nil || rel == "." || filepath.IsLocal(rel) {
		return nil, ErrDenied
	}
	root, err := os.OpenRoot(binding.Root)
	if err != nil {
		return nil, err
	}
	w := &Workspace{binding: binding, root: root, directory: abs, observer: observer}
	if err = w.checkRoot(); err != nil {
		root.Close()
		return nil, err
	}
	return w, nil
}
func (w *Workspace) Close() error { return w.root.Close() }
func (w *Workspace) checkRoot() error {
	f, err := w.root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	id, _, err := identity(f, info)
	if err != nil {
		return err
	}
	if id != w.binding.Identity {
		return ErrDenied
	}
	return nil
}
func validPath(path string) bool {
	if path == "" || len(path) > 1024 || !utf8.ValidString(path) || strings.ContainsAny(path, "\\:\x00~") || strings.HasPrefix(path, "/") {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > 32 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, ". ") != part {
			return false
		}
		lower := strings.ToLower(part)
		base := strings.Split(lower, ".")[0]
		if lower == ".git" || lower == ".hg" || lower == ".svn" || strings.HasPrefix(lower, ".env") || strings.HasPrefix(lower, ".focus-") || lower == ".netrc" || lower == "id_rsa" || lower == "id_ed25519" || base == "con" || base == "prn" || base == "aux" || base == "nul" || base == "conin$" || base == "conout$" || (len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9') {
			return false
		}
		for _, r := range part {
			if r < 32 || strings.ContainsRune("<>\"|?*", r) {
				return false
			}
		}
	}
	return true
}
func allowed(rules []string, path string) bool {
	for _, rule := range rules {
		if rule == "." || rule == path || (strings.HasSuffix(rule, "/") && strings.HasPrefix(path, rule)) {
			return true
		}
	}
	return false
}
func (w *Workspace) inspect(path string) error {
	if !validPath(path) || !allowed(w.binding.Read, path) {
		return ErrDenied
	}
	if err := w.checkRoot(); err != nil {
		return err
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		info, err := w.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if linked(info) {
			return ErrDenied
		}
		if i < len(parts)-1 && !info.IsDir() {
			return ErrDenied
		}
	}
	return nil
}
func (w *Workspace) read(path string) ([]byte, os.FileInfo, error) {
	if err := w.inspect(path); err != nil {
		return nil, nil, err
	}
	f, err := w.root.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || before.Size() > maxFile {
		return nil, nil, ErrDenied
	}
	_, links, err := identity(f, before)
	if err != nil || links != 1 {
		return nil, nil, ErrDenied
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxFile || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return nil, nil, ErrDenied
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(data)) != after.Size() {
		return nil, nil, ErrVersion
	}
	return data, after, nil
}
func (w *Workspace) notify(stage Stage, r domain.Receipt) error {
	if w.observer == nil {
		return nil
	}
	copy, err := domain.Copy(r)
	if err != nil {
		return err
	}
	return w.observer(stage, copy)
}
func tempName(path string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	name := ".focus-tmp-" + hex.EncodeToString(random[:])
	dir := strings.TrimSuffix(filepath.ToSlash(filepath.Dir(path)), ".")
	if dir != "" {
		return dir + "/" + name, nil
	}
	return name, nil
}
func (w *Workspace) syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := w.root.Open(filepath.ToSlash(filepath.Dir(path)))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (w *Workspace) checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.checkRoot()
}
