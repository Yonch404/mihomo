//go:build !with_singbox

package singbox

import "io/fs"

func bundledAssetFiles() (fs.FS, []assetFile, error) {
	return nil, nil, nil
}
