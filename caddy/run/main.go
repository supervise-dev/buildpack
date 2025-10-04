package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/paketo-buildpacks/packit/v2"
)

const (
	layerName     = "caddy"
	xcaddyVersion = "v0.4.5"
)

var caddyPlugins = []string{
	"github.com/ggicci/caddy-jwt",
}

func main() {
	packit.Run(detect, build)
}

func detect(packit.DetectContext) (packit.DetectResult, error) {
	return packit.DetectResult{}, nil
}

func build(context packit.BuildContext) (packit.BuildResult, error) {
	plugins := append([]string(nil), caddyPlugins...)
	sort.Strings(plugins)

	metadataHash := sha256.Sum256([]byte(xcaddyVersion + ":" + strings.Join(plugins, ",")))
	buildHash := hex.EncodeToString(metadataHash[:])

	layer, err := context.Layers.Get(layerName)
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to get %s layer: %w", layerName, err)
	}

	binDir := filepath.Join(layer.Path, "bin")
	caddyPath := filepath.Join(binDir, "caddy")

	if cachedHash, ok := layer.Metadata["build_hash"].(string); ok {
		if cachedVersion, ok := layer.Metadata["buildpack_version"].(string); ok && cachedVersion == context.BuildpackInfo.Version && cachedHash == buildHash {
			if cachedCaddyVersion, ok := layer.Metadata["caddy_version"].(string); ok && fileExists(caddyPath) {
				layer.Launch = true
				layer.Cache = true
				layer.Build = true

				// Always copy config even when using cache to pick up config changes
				if err := copyDefaultConfig(context.CNBPath, layer.Path); err != nil {
					return packit.BuildResult{}, err
				}

				if err := writeSBOM(layer.Path, buildHash, plugins, cachedCaddyVersion); err != nil {
					return packit.BuildResult{}, err
				}

				return packit.BuildResult{
					Layers: []packit.Layer{layer},
				}, nil
			}
		}
	}

	layer, err = layer.Reset()
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to reset %s layer: %w", layer.Name, err)
	}

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to create bin directory: %w", err)
	}

	osName := runtime.GOOS
	arch := runtime.GOARCH

	archiveURL := fmt.Sprintf("https://github.com/caddyserver/xcaddy/releases/download/%s/xcaddy_%s_%s_%s.tar.gz", xcaddyVersion, strings.TrimPrefix(xcaddyVersion, "v"), osName, arch)

	data, err := download(archiveURL)
	if err != nil {
		return packit.BuildResult{}, err
	}

	if err := extractTarGz(data, binDir); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to extract xcaddy archive: %w", err)
	}

	xcaddyPath := filepath.Join(binDir, "xcaddy")
	if err := os.Chmod(xcaddyPath, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to make xcaddy executable: %w", err)
	}

	if err := runXCaddy(binDir, xcaddyPath, caddyPath, plugins); err != nil {
		return packit.BuildResult{}, err
	}

	if err := os.Remove(xcaddyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return packit.BuildResult{}, fmt.Errorf("failed to remove xcaddy binary: %w", err)
	}

	caddyVersion, err := commandOutput(caddyPath, "version")
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to determine caddy version: %w", err)
	}

	if err := copyDefaultConfig(context.CNBPath, layer.Path); err != nil {
		return packit.BuildResult{}, err
	}

	layer.Launch = true
	layer.Build = true
	layer.Cache = true

	layer.Metadata = map[string]interface{}{
		"build_hash":        buildHash,
		"xcaddy_version":    xcaddyVersion,
		"plugins":           strings.Join(plugins, ","),
		"caddy_version":     caddyVersion,
		"buildpack_version": context.BuildpackInfo.Version,
		"uri":               archiveURL,
	}

	if err := writeSBOM(layer.Path, buildHash, plugins, caddyVersion); err != nil {
		return packit.BuildResult{}, err
	}

	return packit.BuildResult{
		Layers: []packit.Layer{layer},
	}, nil
}

func writeSBOM(layerPath, buildHash string, plugins []string, version string) error {
	sbom := map[string]interface{}{
		"name": "caddy",
		"metadata": map[string]interface{}{
			"build_hash":     buildHash,
			"xcaddy_version": xcaddyVersion,
			"plugins":        strings.Join(plugins, ","),
			"version":        version,
		},
	}

	sbomData, err := json.MarshalIndent(sbom, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal SBOM: %w", err)
	}

	sbomPath := filepath.Join(layerPath, "sbom.json")
	if err := os.WriteFile(sbomPath, sbomData, 0o644); err != nil {
		return fmt.Errorf("failed to write SBOM file: %w", err)
	}

	return nil
}

func download(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to download xcaddy from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xcaddy download returned status %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read xcaddy archive: %w", err)
	}

	return data, nil
}

func extractTarGz(data []byte, dest string) error {
	gzReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		target := filepath.Join(dest, header.Name)
		if err := ensureWithinDir(dest, target); err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("failed to create symlink %s -> %s: %w", target, header.Linkname, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", target, err)
			}

			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", target, err)
			}

			if _, err := io.Copy(file, tarReader); err != nil {
				file.Close()
				return fmt.Errorf("failed to write file %s: %w", target, err)
			}

			if err := file.Close(); err != nil {
				return fmt.Errorf("failed to close file %s: %w", target, err)
			}
		default:
			return fmt.Errorf("unsupported tar entry %s of type %v", header.Name, header.Typeflag)
		}
	}
}

func ensureWithinDir(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)

	if !strings.HasPrefix(target, root+string(os.PathSeparator)) && target != root {
		return fmt.Errorf("archive entry escapes destination: %s", target)
	}

	return nil
}

func runXCaddy(binDir, xcaddyPath, outputPath string, plugins []string) error {
	args := []string{xcaddyPath, "build", "--output", outputPath}
	for _, plugin := range plugins {
		args = append(args, "--with", plugin)
	}

	cmd := exec.Command("pkgx", append([]string{"+go"}, args...)...)
	cmd.Dir = binDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xcaddy build failed: %w", err)
	}

	if err := os.Chmod(outputPath, 0o755); err != nil {
		return fmt.Errorf("failed to make caddy executable: %w", err)
	}

	return nil
}

func commandOutput(command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return strings.TrimSpace(string(output)), nil
}

func copyDefaultConfig(cnbPath, layerPath string) error {
	source := filepath.Join(cnbPath, "config", "Caddyfile")
	destDir := filepath.Join(layerPath, "config")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	dest := filepath.Join(destDir, "Caddyfile")

	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("failed to read default Caddyfile: %w", err)
	}

	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("failed to write Caddyfile: %w", err)
	}

	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return !info.IsDir()
}
