// package config

import (
	"bytes"
	"path/filepath"
	"strings"
	"text/template"

	cmtos "github.com/cometbft/cometbft/libs/os"
)

// DefaultDirPerm is the default permissions (rwx------) used when creating configuration directories.
const DefaultDirPerm = 0700

// configTemplate holds the parsed TOML template used to generate the config file.
var configTemplate *template.Template

func init() {
	var err error
	
	// Initialize a new template with custom functions.
	tmpl := template.New("configFileTemplate").Funcs(template.FuncMap{
		// Used to join string slices into a comma-separated string for TOML lists.
		"StringsJoin": strings.Join,
	})
	
	// Parse the default configuration template string.
	if configTemplate, err = tmpl.Parse(defaultConfigTemplate); err != nil {
		// Panic if the default configuration template cannot be parsed, as this is a fatal error.
		panic(err)
	}
}

// --- Configuration File Management Functions ---

/**
 * @title EnsureRoot
 * @notice Creates the application's root, config, and data directories if they do not exist.
 * @param rootDir The path to the application's home directory (e.g., "$HOME/.cometbft").
 */
func EnsureRoot(rootDir string) {
	// Ensure the root directory exists.
	if err := cmtos.EnsureDir(rootDir, DefaultDirPerm); err != nil {
		panic(err.Error())
	}
	// Ensure the configuration directory exists inside the root.
	if err := cmtos.EnsureDir(filepath.Join(rootDir, DefaultConfigDir), DefaultDirPerm); err != nil {
		panic(err.Error())
	}
	// Ensure the data directory exists inside the root.
	if err := cmtos.EnsureDir(filepath.Join(rootDir, DefaultDataDir), DefaultDirPerm); err != nil {
		panic(err.Error())
	}

	configFilePath := filepath.Join(rootDir, defaultConfigFilePath)

	// Write the default config file only if it is missing.
	if !cmtos.FileExists(configFilePath) {
		writeDefaultConfigFile(configFilePath)
	}
}

/**
 * @title writeDefaultConfigFile
 * @notice Helper function to write the default configuration to the specified path.
 * @param configFilePath The full path where the TOML config file should be written.
 */
func writeDefaultConfigFile(configFilePath string) {
	// Use the application's default settings struct.
	WriteConfigFile(configFilePath, DefaultConfig())
}

/**
 * @title WriteConfigFile
 * @notice Renders the configuration struct into the TOML format using the template and writes it to disk.
 * @param configFilePath The destination file path.
 * @param config The struct containing the configuration values to be rendered.
 */
func WriteConfigFile(configFilePath string, config *Config) {
	var buffer bytes.Buffer

	// Execute the template against the config struct.
	if err := configTemplate.Execute(&buffer, config); err != nil {
		panic(err)
	}

	// Write the buffer content to the file path with standard file permissions (rw-r--r--).
	cmtos.MustWriteFile(configFilePath, buffer.Bytes(), 0644)
}

// defaultConfigTemplate (TOML template string follows here...)
// [Content of defaultConfigTemplate goes here, unchanged]
const defaultConfigTemplate = `# This is a TOML config file.
... (Template content is large and omitted for brevity) ...
`
