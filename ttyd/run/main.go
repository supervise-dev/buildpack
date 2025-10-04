package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/paketo-buildpacks/packit/v2"
)

const (
	layerName       = "ttyd"
	defaultVersion  = "1.7.7"
	releasesBaseURL = "https://github.com/tsl0922/ttyd/releases/download"
)

var assetMap = map[string]string{
	"linux/amd64": "ttyd.x86_64",
	"linux/arm64": "ttyd.aarch64",
}

func main() {
	packit.Run(detect, build)
}

func detect(packit.DetectContext) (packit.DetectResult, error) {
	return packit.DetectResult{}, nil
}

func build(context packit.BuildContext) (packit.BuildResult, error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	assetKey := fmt.Sprintf("%s/%s", osName, arch)
	assetName, ok := assetMap[assetKey]
	if !ok {
		return packit.BuildResult{}, fmt.Errorf("unsupported platform %s", assetKey)
	}

	version := strings.TrimSpace(os.Getenv("TTYD_VERSION"))
	if version == "" {
		version = defaultVersion
	}

	archiveURL := fmt.Sprintf("%s/%s/%s", releasesBaseURL, version, assetName)

	layer, err := context.Layers.Get(layerName)
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to get %s layer: %w", layerName, err)
	}

	layer, err = layer.Reset()
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to reset %s layer: %w", layer.Name, err)
	}

	binDir := filepath.Join(layer.Path, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to create bin directory: %w", err)
	}

	binaryPath := filepath.Join(binDir, "ttyd")

	data, checksum, err := fetchBinary(archiveURL)
	if err != nil {
		return packit.BuildResult{}, err
	}

	if err := os.WriteFile(binaryPath, data, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to write ttyd binary: %w", err)
	}

	layer.Launch = true
	layer.Build = true
	layer.Cache = true

	layer.Metadata = map[string]interface{}{
		"checksum":          checksum,
		"uri":               archiveURL,
		"version":           version,
		"asset":             assetName,
		"os":                osName,
		"arch":              arch,
		"buildpack_version": context.BuildpackInfo.Version,
	}

	return packit.BuildResult{
		Layers: []packit.Layer{layer},
	}, nil
}

func fetchBinary(url string) ([]byte, string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download ttyd from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("ttyd download returned status %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read ttyd binary: %w", err)
	}

	sum := sha256.Sum256(data)

	return data, hex.EncodeToString(sum[:]), nil
}
