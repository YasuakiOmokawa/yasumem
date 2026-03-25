package main

import (
	"encoding/json"
	"os"
)

func runRecall() {
	dbPath := getDBPath()

	var input struct {
		Cwd string `json:"cwd"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil || input.Cwd == "" {
		os.Exit(0)
	}

	canonical := resolveCanonicalProject(input.Cwd)

	db, err := openDB(dbPath)
	if err != nil {
		os.Exit(0)
	}
	defer db.Close()

	context := recall(db, canonical, 5)
	if context == "" {
		os.Exit(0)
	}

	response := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": context,
		},
	}
	json.NewEncoder(os.Stdout).Encode(response)
}
