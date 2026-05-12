//go:build !with_singbox

package singbox

func bundledAssetFiles() ([]assetFile, error) {
	return nil, nil
}
