package util

import (
	"encoding/json"
	"fmt"
	"io"

	"sigs.k8s.io/yaml"
)

// OutputFormat represents the requested output format for a command.
type OutputFormat string

const (
	OutputTable OutputFormat = "table"
	OutputWide  OutputFormat = "wide"
	OutputJSON  OutputFormat = "json"
	OutputYAML  OutputFormat = "yaml"
)

// PrintJSON serialises obj to JSON and writes it to w.
func PrintJSON(w io.Writer, obj any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(obj); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	return nil
}

// PrintYAML serialises obj to YAML and writes it to w.
func PrintYAML(w io.Writer, obj any) error {
	b, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("encoding YAML: %w", err)
	}
	_, err = w.Write(b)
	return err
}
