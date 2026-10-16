// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// OrganizationsCSVFileName is the organization mapping file the operator
// edits by hand before running migrate.
const OrganizationsCSVFileName = "organizations.csv"

// backupTimeLayout keeps backup names sortable and free of characters
// that are awkward on Windows file systems.
const backupTimeLayout = "20060102-150405"

// EditableCSVs lists the files structure writes that an operator is
// expected to edit by hand: sonarcloud_org_key in organizations.csv and,
// since #612, in projects.csv.
var EditableCSVs = []string{OrganizationsCSVFileName, ProjectsCSVFileName}

// BackupEditableCSVs copies each existing, non-empty file in EditableCSVs
// to <name>.bak-<timestamp> in the same directory, before structure
// regenerates it (#503). extract now runs structure itself, so a second
// extract would otherwise overwrite an organizations.csv the operator
// already filled in. The timestamp keeps every earlier backup, so a third
// run cannot overwrite the backup holding the operator's edits.
//
// It returns the paths of the backups it wrote. A file that does not exist
// is skipped. A backup name that already exists is an error, never an
// overwrite.
func BackupEditableCSVs(dir string, now time.Time) ([]string, error) {
	suffix := ".bak-" + now.Format(backupTimeLayout)
	var written []string
	for _, name := range EditableCSVs {
		src := filepath.Join(dir, name)
		data, err := os.ReadFile(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return written, fmt.Errorf("reading %s for backup: %w", src, err)
		}
		if len(data) == 0 {
			continue
		}
		dst := src + suffix
		if err := writeNewFile(dst, data); err != nil {
			return written, fmt.Errorf("backing up %s: %w", src, err)
		}
		written = append(written, dst)
	}
	return written, nil
}

// writeNewFile writes data to path, failing if path already exists.
func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
