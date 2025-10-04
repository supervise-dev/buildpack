package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paketo-buildpacks/packit/v2"
)

const layerName = "pkgx"

func main() {
	packit.Run(detect, build)
}

func detect(context packit.DetectContext) (packit.DetectResult, error) {
	return packit.DetectResult{}, nil
}

func build(context packit.BuildContext) (packit.BuildResult, error) {
	osName, err := uname()
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to determine operating system: %w", err)
	}

	arch, err := uname("-m")
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to determine architecture: %w", err)
	}

	archiveURL := fmt.Sprintf("https://pkgx.sh/%s/%s.tgz", osName, arch)

	layer, err := context.Layers.Get(layerName)
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to get layer: %w", err)
	}

	layer, err = layer.Reset()
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to reset layer: %w", err)
	}

	binDir := filepath.Join(layer.Path, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to create bin directory: %w", err)
	}

	data, checksum, err := fetchArchive(archiveURL)
	if err != nil {
		return packit.BuildResult{}, err
	}

	if err := extractTarGz(data, binDir); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to extract pkgx archive: %w", err)
	}

	pkgxBinary := filepath.Join(binDir, "pkgx")
	if err := os.Chmod(pkgxBinary, 0o755); err != nil && !errors.Is(err, os.ErrNotExist) {
		return packit.BuildResult{}, fmt.Errorf("failed to ensure pkgx executable permissions: %w", err)
	}

	layer.Launch = true
	layer.Build = true
	layer.Cache = true

	layer.Metadata = map[string]interface{}{
		"checksum":          checksum,
		"uri":               archiveURL,
		"os":                osName,
		"arch":              arch,
		"buildpack_version": context.BuildpackInfo.Version,
	}

	return packit.BuildResult{
		Layers: []packit.Layer{layer},
	}, nil
}

func uname(args ...string) (string, error) {
	cmd := exec.Command("uname", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func fetchArchive(url string) ([]byte, string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download pkgx archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("pkgx download returned status %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read pkgx archive: %w", err)
	}

	checksum := sha256.Sum256(data)

	return data, fmt.Sprintf("%x", checksum), nil
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
				return fmt.Errorf("failed to copy file %s: %w", target, err)
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
