package cmd

import "fmt"

// DefaultExportDirectory is the implicit value of --export_directory
// when the operator passes neither a flag nor a config-file value
// (issue #247). Relative path so the tool creates the directory in
// whatever cwd the operator invoked it from.
const DefaultExportDirectory = "./migration-files"

// printExportDirNotice writes a uniform "See sonar-migration-tool
// output results in <dir>" line so every command tells the operator
// where to look — whether the directory was defaulted, supplied via
// --export_directory, or read from the config file.
func printExportDirNotice(dir string) {
	if dir == "" {
		return
	}
	fmt.Printf("See sonar-migration-tool output results in %s\n", dir)
}
