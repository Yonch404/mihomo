package updater

import (
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoreBaseNameUsesConfiguredPackagePrefix(t *testing.T) {
	name := DefaultCoreUpdater.coreBaseName("mihomo-singbox")

	require.True(t, strings.HasPrefix(name, "mihomo-singbox-"+runtime.GOOS+"-"+runtime.GOARCH))
}

func TestGzFileUnpackWritesExplicitOutputPath(t *testing.T) {
	dir := t.TempDir()
	packagePath := filepath.Join(dir, "mihomo-singbox-test-version.gz")
	outputPath := filepath.Join(dir, "mihomo-singbox-test")

	file, err := os.Create(packagePath)
	require.NoError(t, err)
	writer := gzip.NewWriter(file)
	_, err = writer.Write([]byte("new core"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())

	outputName, err := DefaultCoreUpdater.gzFileUnpack(packagePath, outputPath, 0o755)
	require.NoError(t, err)
	require.Equal(t, outputPath, outputName)

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.Equal(t, "new core", string(data))
}

func TestZipFileUnpackWritesExplicitOutputPath(t *testing.T) {
	dir := t.TempDir()
	packagePath := filepath.Join(dir, "mihomo-singbox-test-version.zip")
	outputPath := filepath.Join(dir, "mihomo-singbox-test.exe")

	file, err := os.Create(packagePath)
	require.NoError(t, err)
	writer := zip.NewWriter(file)
	entry, err := writer.Create("any-release-name.exe")
	require.NoError(t, err)
	_, err = entry.Write([]byte("new windows core"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())

	outputName, err := DefaultCoreUpdater.zipFileUnpack(packagePath, outputPath, 0o755)
	require.NoError(t, err)
	require.Equal(t, outputPath, outputName)

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.Equal(t, "new windows core", string(data))
}
