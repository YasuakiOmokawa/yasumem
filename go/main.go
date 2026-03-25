package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func getDBPath() string {
	if v := os.Getenv("YASUMEM_DB"); v != "" {
		return v
	}
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "..", "data", "memory.db")
}

func getCurrentProject() string {
	exe, _ := os.Executable()
	p := filepath.Join(filepath.Dir(exe), "..", "data", "current_project")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: yasumem <server|ingest|ingest-recent|recall>")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "server":
		runServer()
	case "ingest":
		runIngest()
	case "ingest-recent":
		runIngestRecent()
	case "recall":
		runRecall()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
