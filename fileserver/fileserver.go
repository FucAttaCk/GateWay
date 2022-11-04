package fileserver

import (
	"encoding/json"
	"errors"
	"github.com/FucAttaCk/gateway/util"
	"github.com/megaease/easegress/pkg/context"
	"github.com/megaease/easegress/pkg/object/httppipeline"
	"github.com/nacos-group/nacos-sdk-go/common/logger"
	"go.uber.org/zap"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	// Kind is the kind of FileServer.
	Kind = "FileServer"

	separator = string(filepath.Separator)

	resultIllegalADSPath   = "illegalADSPath"
	resultIllegalShortName = "illegalShortName"
	resultNotFound         = "notFound"
	resultErrPermission    = "errPermission"
	resultErrHandleFile    = "errHandleFile"
	resultMethodNotAllowed = "methodNotAllowed"
)

var (
	results = []string{resultIllegalADSPath, resultIllegalShortName, resultMethodNotAllowed,
		resultNotFound, resultErrPermission, resultErrHandleFile}
	repl               = util.NewReplacer()
	_    fs.StatFS     = (*osFS)(nil)
	_    fs.GlobFS     = (*osFS)(nil)
	_    fs.ReadDirFS  = (*osFS)(nil)
	_    fs.ReadFileFS = (*osFS)(nil)
)

func init() {
	httppipeline.Register(&FileServer{})
}

type osFS struct{}

func (osFS) Open(name string) (fs.File, error)          { return os.Open(name) }
func (osFS) Stat(name string) (fs.FileInfo, error)      { return os.Stat(name) }
func (osFS) Glob(pattern string) ([]string, error)      { return filepath.Glob(pattern) }
func (osFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (osFS) ReadFile(name string) ([]byte, error)       { return os.ReadFile(name) }

type (

	// Spec is the spec of file server
	Spec struct {
		FileSystemRaw json.RawMessage
		fileSystem    fs.FS
		Root          string
		Hide          []string
		// The names of files to try as index files if a folder is requested.
		// Default: index.html, index.txt.
		IndexNames []string
	}

	FileServer struct {
		filterSpec *httppipeline.FilterSpec
		spec       *Spec
	}
)

// Kind returns the kind of FileServer.
func (fsrv *FileServer) Kind() string {
	return Kind
}

// DefaultSpec returns the default spec of FileServer.
func (fsrv *FileServer) DefaultSpec() interface{} {
	return &Spec{
		IndexNames: []string{"index.html", "index.txt"},
		fileSystem: &osFS{},
	}
}

// Description returns the description of FileServer
func (fsrv *FileServer) Description() string {
	return "FileServer implements a static files for http request."
}

// Results returns the results of FileServer.
func (fsrv *FileServer) Results() []string {
	return results
}

// Init initializes FileServer.
func (fsrv *FileServer) Init(filterSpec *httppipeline.FilterSpec) {
	fsrv.filterSpec = filterSpec
	fsrv.spec = filterSpec.FilterSpec().(*Spec)
}

// Inherit inherits previous generation of FileServer.
func (fsrv *FileServer) Inherit(filterSpec *httppipeline.FilterSpec, previousGeneration httppipeline.Filter) {
	fsrv.Init(filterSpec)
}

// Handle handles HTTP request
func (fsrv *FileServer) Handle(ctx context.HTTPContext) string {
	res := fsrv.handle(ctx)
	return ctx.CallNextHandler(res)
}

func (fsrv *FileServer) handle(ctx context.HTTPContext) string {
	r := ctx.Request()
	w := ctx.Response()
	p := r.Path()

	if runtime.GOOS == "windows" {
		// reject paths with Alternate Data Streams (ADS)
		if strings.Contains(p, ":") {
			ctx.AddTag("illegal ADS path")
			w.SetStatusCode(http.StatusBadRequest)
			return resultIllegalADSPath
		}
		// reject paths with "8.3" short names
		trimmedPath := strings.TrimRight(p, ". ") // Windows ignores trailing dots and spaces, sigh
		if len(path.Base(trimmedPath)) <= 12 && strings.Contains(trimmedPath, "~") {
			ctx.AddTag("illegal short name")
			w.SetStatusCode(http.StatusBadRequest)
			return resultIllegalShortName
		}
	}

	filesToHide := fsrv.transformHidePaths(repl)

	root := repl.ReplaceAll(fsrv.spec.Root, ".")

	filename := util.SanitizedPathJoin(root, p)

	logger.Debug("sanitized path join",
		zap.String("site_root", root),
		zap.String("request_path", p),
		zap.String("result", filename))

	// get information about the file
	info, err := fs.Stat(fsrv.spec.fileSystem, filename)
	if err != nil {
		err = fsrv.mapDirOpenError(err, filename)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			ctx.AddTag("not found")
			w.SetStatusCode(http.StatusNotFound)
			return resultNotFound
		} else if errors.Is(err, fs.ErrPermission) {
			ctx.AddTag(err.Error())
			w.SetStatusCode(http.StatusForbidden)
			return resultErrPermission
		}
		ctx.AddTag(err.Error())
		w.SetStatusCode(http.StatusInternalServerError)
		return resultErrHandleFile
	}

	// if the r mapped to a directory, see if
	// there is an index file we can serve
	if info.IsDir() && len(fsrv.spec.IndexNames) > 0 {
		for _, indexPage := range fsrv.spec.IndexNames {
			indexPage := repl.ReplaceAll(indexPage, "")
			indexPath := util.SanitizedPathJoin(filename, indexPage)
			if fileHidden(indexPath, filesToHide) {
				// pretend this file doesn't exist
				logger.Debug("hiding index file",
					zap.String("filename", indexPath),
					zap.Strings("files_to_hide", filesToHide))
				continue
			}

			indexInfo, err := fs.Stat(fsrv.spec.fileSystem, indexPath)
			if err != nil {
				continue
			}

			// don't rewrite the r path to append
			// the index file, because we might need to
			// do a canonical-URL redirect below based
			// on the URL as-is

			// we've chosen to use this index file,
			// so replace the last file info and path
			// with that of the index file
			info = indexInfo
			filename = indexPath
			logger.Debug("located index file", zap.String("filename", filename))
			break
		}
	}

	// if still referencing a directory, delegate
	// to browse or return an error
	if info.IsDir() {
		logger.Debug("no index file in directory",
			zap.String("path", filename),
			zap.Strings("index_filenames", fsrv.spec.IndexNames))
		ctx.AddTag("not found")
		w.SetStatusCode(http.StatusNotFound)
		return resultNotFound
	}

	// one last check to ensure the file isn't hidden (we might
	// have changed the filename from when we last checked)
	if fileHidden(filename, filesToHide) {
		logger.Debug("hiding file",
			zap.String("filename", filename),
			zap.Strings("files_to_hide", filesToHide))

		ctx.AddTag("not found")

		w.SetStatusCode(http.StatusNotFound)
		return resultNotFound
	}

	var file fs.File
	var etag string

	// no precompressed file found, use the actual file
	if file == nil {
		logger.Debug("opening file", zap.String("filename", filename))

		// open the file
		file, err = fsrv.openFile(filename)
		if err != nil {
			err = fsrv.mapDirOpenError(err, filename)
			if os.IsNotExist(err) {
				logger.Debug("file not found", zap.String("filename", filename), zap.Error(err))
				ctx.AddTag("not found")
				w.SetStatusCode(http.StatusNotFound)
				return resultNotFound
			} else if os.IsPermission(err) {
				logger.Debug("permission denied", zap.String("filename", filename), zap.Error(err))

				ctx.AddTag("permission denied")
				w.SetStatusCode(http.StatusForbidden)
				return resultErrPermission

			}
			ctx.AddTag(err.Error())
			w.SetStatusCode(http.StatusInternalServerError)
			return resultErrHandleFile
		}
		defer file.Close()

		etag = calculateEtag(info)
	}
	method := ctx.Request().Method()
	// at this point, we're serving a file; Go std lib supports only
	// GET and HEAD, which is sensible for a static file server - reject
	// any other methods (see issue #5166)
	if method != http.MethodGet && method != http.MethodHead {
		w.Header().Add("Allow", "GET, HEAD")
		w.SetStatusCode(http.StatusMethodNotAllowed)
		return resultMethodNotAllowed

	}

	// set the Etag - note that a conditional If-None-Match r is handled
	// by http.ServeContent below, which checks against this Etag value
	w.Header().Set("Etag", etag)

	if w.Header().Get("Content-Type") == "" {
		mtyp := mime.TypeByExtension(filepath.Ext(filename))
		if mtyp == "" {
			// do not allow Go to sniff the content-type; see https://www.youtube.com/watch?v=8t8JYpt0egE
			w.Header().Del("Content-Type")
		} else {
			w.Header().Set("Content-Type", mtyp)
		}
	}

	// let the standard library do what it does best; note, however,
	// that errors generated by ServeContent are written immediately
	// to the response, so we cannot handle them (but errors there
	// are rare)
	http.ServeContent(w.Std(), r.Std(), info.Name(), info.ModTime(), file.(io.ReadSeeker))

	return ""
}

// calculateEtag produces a strong etag by default, although, for
// efficiency reasons, it does not actually consume the contents
// of the file to make a hash of all the bytes. ¯\_(ツ)_/¯
// Prefix the etag with "W/" to convert it into a weak etag.
// See: https://tools.ietf.org/html/rfc7232#section-2.3
func calculateEtag(d os.FileInfo) string {
	t := strconv.FormatInt(d.ModTime().Unix(), 36)
	s := strconv.FormatInt(d.Size(), 36)
	return `"` + t + s + `"`
}

func (fsrv *FileServer) openFile(filename string) (fs.File, error) {
	file, err := fsrv.spec.fileSystem.Open(filename)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// fileHidden returns true if filename is hidden according to the hide list.
// filename must be a relative or absolute file system path, not a request
// URI path. It is expected that all the paths in the hide list are absolute
// paths or are singular filenames (without a path separator).
func fileHidden(filename string, hide []string) bool {
	if len(hide) == 0 {
		return false
	}

	// all path comparisons use the complete absolute path if possible
	filenameAbs, err := filepath.Abs(filename)
	if err == nil {
		filename = filenameAbs
	}

	var components []string

	for _, h := range hide {
		if !strings.Contains(h, separator) {
			// if there is no separator in h, then we assume the user
			// wants to hide any files or folders that match that
			// name; thus we have to compare against each component
			// of the filename, e.g. hiding "bar" would hide "/bar"
			// as well as "/foo/bar/baz" but not "/barstool".
			if len(components) == 0 {
				components = strings.Split(filename, separator)
			}
			for _, c := range components {
				if hidden, _ := filepath.Match(h, c); hidden {
					return true
				}
			}
		} else if strings.HasPrefix(filename, h) {
			// if there is a separator in h, and filename is exactly
			// prefixed with h, then we can do a prefix match so that
			// "/foo" matches "/foo/bar" but not "/foobar".
			withoutPrefix := strings.TrimPrefix(filename, h)
			if strings.HasPrefix(withoutPrefix, separator) {
				return true
			}
		}

		// in the general case, a glob match will suffice
		if hidden, _ := filepath.Match(h, filename); hidden {
			return true
		}
	}

	return false
}

// mapDirOpenError maps the provided non-nil error from opening name
// to a possibly better non-nil error. In particular, it turns OS-specific errors
// about opening files in non-directories into os.ErrNotExist. See golang/go#18984.
// Adapted from the Go standard library; originally written by Nathaniel Caza.
// https://go-review.googlesource.com/c/go/+/36635/
// https://go-review.googlesource.com/c/go/+/36804/
func (fsrv *FileServer) mapDirOpenError(originalErr error, name string) error {
	if errors.Is(originalErr, fs.ErrNotExist) || errors.Is(originalErr, fs.ErrPermission) {
		return originalErr
	}

	parts := strings.Split(name, separator)
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		fi, err := fs.Stat(fsrv.spec.fileSystem, strings.Join(parts[:i+1], separator))
		if err != nil {
			return originalErr
		}
		if !fi.IsDir() {
			return fs.ErrNotExist
		}
	}

	return originalErr
}

func (fsrv *FileServer) transformHidePaths(repl *util.Replacer) []string {
	hide := make([]string, len(fsrv.spec.Hide))
	for i := range fsrv.spec.Hide {
		hide[i] = repl.ReplaceAll(fsrv.spec.Hide[i], "")
		if strings.Contains(hide[i], separator) {
			abs, err := filepath.Abs(hide[i])
			if err == nil {
				hide[i] = abs
			}
		}
	}
	return hide
}

// Status returns Status generated by Runtime.
func (fsrv *FileServer) Status() interface{} {
	return nil
}

// Close closes FileServer.
func (fsrv *FileServer) Close() {
}
