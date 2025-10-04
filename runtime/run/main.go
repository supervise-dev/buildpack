package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/paketo-buildpacks/packit/v2"
	"gopkg.in/yaml.v3"
)

const (
	layerName              = "runtime"
	defaultCaddyConfigPath = "/layers/dev.supervise.caddy/caddy/config/Caddyfile"
	defaultCaddyBinaryPath = "/layers/dev.supervise.caddy/caddy/bin/caddy"
)

func main() {
	packit.Run(detect, build)
}

func detect(context packit.DetectContext) (packit.DetectResult, error) {
	// Always pass detection - runtime is always required
	// Attempt to make working directory writable, but don't fail if it errors
	_ = exec.Command("chmod", "-R", "a+w", context.WorkingDir).Run()
	return packit.DetectResult{}, nil
}

func build(context packit.BuildContext) (packit.BuildResult, error) {
	layer, err := context.Layers.Get(layerName)
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to get layer: %w", err)
	}

	layer, err = layer.Reset()
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to reset layer: %w", err)
	}

	// Create necessary directories
	binDir := filepath.Join(layer.Path, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to create bin directory: %w", err)
	}

	configDir := filepath.Join(layer.Path, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to create config directory: %w", err)
	}

	configHome := filepath.Join(configDir, "process-compose")
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to create process-compose config home: %w", err)
	}

	// Copy agent.sh script
	agentScriptSrc := filepath.Join(context.CNBPath, "scripts", "agent.sh")
	agentScriptDst := filepath.Join(binDir, "agent.sh")

	if err := copyFile(agentScriptSrc, agentScriptDst); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to copy agent.sh: %w", err)
	}

	if err := os.Chmod(agentScriptDst, 0o755); err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to make agent.sh executable: %w", err)
	}

	// Read dev process from Procfile
	devCommand, err := readDevProcess(context.WorkingDir)
	if err != nil {
		return packit.BuildResult{}, fmt.Errorf("failed to read dev process: %w", err)
	}

	processComposePath := filepath.Join(configDir, "process-compose.yaml")
	if err := writeProcessComposeConfig(
		filepath.Join(context.CNBPath, "config", "process-compose.yaml"),
		processComposePath,
		devCommand,
		agentScriptDst,
		defaultCaddyConfigPath,
	); err != nil {
		return packit.BuildResult{}, err
	}

	layer.Launch = true
	layer.Cache = false
	layer.Build = true

	layer.LaunchEnv.Default("PROCESS_COMPOSE_HOME", configHome)
	layer.LaunchEnv.Default("TERM", "xterm-256color")
	layer.LaunchEnv.Default("PC_DISABLE_TUI", "1")
	layer.LaunchEnv.Default("PC_LOG_FILE", "/tmp/process-compose.log")
	layer.LaunchEnv.Default("CADDY_CONFIG", defaultCaddyConfigPath)

	layer.Metadata = map[string]interface{}{
		"dev_command": devCommand,
	}

	fmt.Printf("Successfully installed runtime with dev process: %s\n", devCommand)

	// Define the process type that will run process-compose via pkgx
	processComposeCommand := []string{"pkgx"}
	processComposeArgs := []string{"process-compose", "--tui=false", "-f", processComposePath}

	return packit.BuildResult{
		Layers: []packit.Layer{layer},
		Launch: packit.LaunchMetadata{
			DirectProcesses: []packit.DirectProcess{
				{
					Type:    "dev",
					Command: processComposeCommand,
					Args:    processComposeArgs,
					Default: true,
				},
			},
		},
	}, nil
}

func readDevProcess(workingDir string) (string, error) {
	// Look for Procfile in working directory
	procfilePath := filepath.Join(workingDir, "Procfile")

	file, err := os.Open(procfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // No Procfile found, return empty string
		}
		return "", fmt.Errorf("failed to open Procfile: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "dev:") {
			// Extract command after "dev:"
			command := strings.TrimSpace(strings.TrimPrefix(line, "dev:"))
			return command, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to scan Procfile: %w", err)
	}

	return "", nil // No dev process found
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

type processConfig struct {
	Processes map[string]processEntry `yaml:"processes"`
}

type dependencyConfig struct {
	Condition string `yaml:"condition,omitempty"`
}

type processEntry struct {
	Description string                      `yaml:"description,omitempty"`
	Command     string                      `yaml:"command"`
	Args        []string                    `yaml:"args,omitempty"`
	DependsOn   map[string]dependencyConfig `yaml:"depends_on,omitempty"`
	Environment []string                    `yaml:"environment,omitempty"`
}

func writeProcessComposeConfig(templatePath, destPath, devCommand, agentCommand, caddyConfigPath string) error {
	config, err := loadProcessComposeTemplate(templatePath)
	if err != nil {
		return fmt.Errorf("failed to load process-compose template: %w", err)
	}

	processes := config.Processes
	if processes == nil {
		processes = map[string]processEntry{}
	}

	if devCommand != "" {
		processes["dev"] = processEntry{
			Description: "Development process from Procfile",
			Command:     devCommand,
		}
	} else {
		delete(processes, "dev")
	}

	processes["agent"] = processEntry{
		Description: "Supervise agent",
		Command:     agentCommand,
	}

	if _, err := os.Stat(caddyConfigPath); err == nil {
		processes["caddy"] = processEntry{
			Description: "Caddy reverse proxy",
			Command:     fmt.Sprintf("%s run --config %s --adapter caddyfile", defaultCaddyBinaryPath, caddyConfigPath),
			DependsOn: map[string]dependencyConfig{
				"agent": {Condition: "process_started"},
			},
			Environment: []string{
				"XDG_CONFIG_HOME=/tmp", // Use writable directory for Caddy config autosave
			},
		}
	} else {
		delete(processes, "caddy")
	}

	config.Processes = processes

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal process-compose config: %w", err)
	}

	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write process-compose.yaml: %w", err)
	}

	return nil
}

func loadProcessComposeTemplate(path string) (processConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return processConfig{}, nil
		}
		return processConfig{}, err
	}

	var config processConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return processConfig{}, err
	}

	return config, nil
}
