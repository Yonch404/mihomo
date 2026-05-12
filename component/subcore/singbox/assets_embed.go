//go:build with_singbox

package singbox

import "embed"

// Put platform assets under one of these layouts before building with
// -tags with_singbox:
//
//	assets/<goos>_<goarch>/sing-box[.exe]
//	assets/<goos>_<goarch>/libcronet.*
//
// A flat assets/sing-box[.exe] layout is also accepted for single-platform
// local builds.
//
//go:embed assets
var embeddedAssetFS embed.FS

func bundledAssetFiles() ([]assetFile, error) {
	return walkAssetFS(embeddedAssetFS, "assets")
}
