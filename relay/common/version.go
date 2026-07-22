package common

import (
	"fmt"
	"os"
)

var (
	Version     = "0.5.0-alpha.11"
	BuildCommit = "unknown"
	BuildTime   = "unknown"
)

func BuildSummary() string {
	return fmt.Sprintf("version=%s commit=%s built=%s", Version, BuildCommit, BuildTime)
}

func LogBuild(logf func(string, ...any)) {
	logf("[build] %s", BuildSummary())
}

func MaybePrintVersion() {
	for _, arg := range os.Args[1:] {
		if arg == "-version" || arg == "--version" {
			fmt.Println(BuildSummary())
			os.Exit(0)
		}
	}
}
