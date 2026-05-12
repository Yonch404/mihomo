package singbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var ErrAssetsNotEmbedded = errors.New("sing-box subcore assets are not embedded")

type assetFile struct {
	Name string
	Data []byte
}

type assetSet struct {
	Files      []assetFile
	Hash       string
	Executable string
}

func embeddedAvailable() bool {
	assets, err := loadEmbeddedAssets()
	return err == nil && assets.Executable != ""
}

func loadEmbeddedAssets() (*assetSet, error) {
	files, err := bundledAssetFiles()
	if err != nil {
		return nil, err
	}
	return selectPlatformAssets(files), nil
}

func selectPlatformAssets(files []assetFile) *assetSet {
	if len(files) == 0 {
		return &assetSet{}
	}

	executable := executableName()
	for _, dir := range platformAssetDirs() {
		prefix := dir + "/"
		var selected []assetFile
		var foundExecutable bool
		for _, file := range files {
			if strings.HasPrefix(file.Name, prefix) {
				name := strings.TrimPrefix(file.Name, prefix)
				selected = append(selected, assetFile{Name: name, Data: file.Data})
				if path.Base(name) == executable {
					foundExecutable = true
				}
			}
		}
		if foundExecutable {
			return newAssetSet(selected, executable)
		}
	}

	var root []assetFile
	var foundExecutable bool
	for _, file := range files {
		if strings.Contains(file.Name, "/") {
			continue
		}
		root = append(root, file)
		if path.Base(file.Name) == executable {
			foundExecutable = true
		}
	}
	if foundExecutable {
		return newAssetSet(root, executable)
	}

	return &assetSet{}
}

func newAssetSet(files []assetFile, executable string) *assetSet {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	hash := sha256.New()
	_, _ = hash.Write([]byte(runtime.GOOS))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(runtime.GOARCH))
	for _, file := range files {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(file.Name))
		_, _ = hash.Write([]byte{0})
		sum := sha256.Sum256(file.Data)
		_, _ = hash.Write(sum[:])
	}

	return &assetSet{
		Files:      files,
		Hash:       hex.EncodeToString(hash.Sum(nil)),
		Executable: executable,
	}
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "sing-box.exe"
	}
	return "sing-box"
}

func platformAssetDirs() []string {
	return []string{
		runtime.GOOS + "_" + runtime.GOARCH,
		runtime.GOOS + "-" + runtime.GOARCH,
		runtime.GOOS,
	}
}

func isIgnoredAsset(name string) bool {
	base := path.Base(name)
	return base == "README.md" || base == ".gitkeep"
}

func toLocalPath(slashPath string) string {
	return filepath.FromSlash(path.Clean(slashPath))
}

func walkAssetFS(assetFS fs.FS, root string) ([]assetFile, error) {
	var files []assetFile
	err := fs.WalkDir(assetFS, root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(name, root), "/")
		if rel == "" || isIgnoredAsset(rel) {
			return nil
		}
		data, err := fs.ReadFile(assetFS, name)
		if err != nil {
			return err
		}
		files = append(files, assetFile{Name: path.Clean(rel), Data: data})
		return nil
	})
	return files, err
}
