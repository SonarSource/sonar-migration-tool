// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"fmt"
	"os"
)

// ParseJSONConfigFile reads path and unmarshals it into a fresh T,
// erroring on a missing/unreadable file, an empty file, or invalid
// JSON. Shared by extract and migrate's own parseConfigFile wrappers,
// which were byte-for-byte identical aside from the concrete
// config-file-shape type each package unmarshals into.
func ParseJSONConfigFile[T any](path string) (T, error) {
	var shape T
	data, err := os.ReadFile(path)
	if err != nil {
		return shape, fmt.Errorf("reading config file: %w", err)
	}
	if len(data) == 0 {
		return shape, fmt.Errorf("config file %s is empty", path)
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return shape, fmt.Errorf("parsing config file: %w", err)
	}
	return shape, nil
}
