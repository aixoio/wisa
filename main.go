package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	_ "github.com/mattn/go-sqlite3"
)

// WindowState represents the position and size of a window
type WindowState struct {
	AppName     string
	WindowTitle string
	X           float64
	Y           float64
	Width       float64
	Height      float64
}

// Database operations
func getDBPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Error getting home directory: %v", err)
	}
	return filepath.Join(homeDir, "wisa.db")
}

func initDB() *sql.DB {
	dbPath := getDBPath()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}

	// Create tables if they don't exist yet
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE
	);
	CREATE TABLE IF NOT EXISTS window_states (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		profile_id INTEGER NOT NULL,
		app_name TEXT NOT NULL,
		window_title TEXT NOT NULL,
		x REAL NOT NULL,
		y REAL NOT NULL,
		width REAL NOT NULL,
		height REAL NOT NULL,
		FOREIGN KEY (profile_id) REFERENCES profiles(id)
	);
	`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatalf("Error creating tables: %v", err)
	}

	return db
}

// Profile structure to hold both id and name
type Profile struct {
	ID   int
	Name string
}

func saveWindowStates(db *sql.DB, profileName string, states []WindowState) error {
	// First, ensure the profile exists
	var profileID int

	// Try to get existing profile ID
	err := db.QueryRow("SELECT id FROM profiles WHERE name = ?", profileName).Scan(&profileID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Profile doesn't exist, create it
			result, err := db.Exec("INSERT INTO profiles (name) VALUES (?)", profileName)
			if err != nil {
				return fmt.Errorf("error creating profile: %v", err)
			}

			// Get the ID of the newly created profile
			id, err := result.LastInsertId()
			if err != nil {
				return fmt.Errorf("error getting new profile ID: %v", err)
			}
			profileID = int(id)
		} else {
			return fmt.Errorf("error checking if profile exists: %v", err)
		}
	}

	// Delete any existing window states for this profile
	_, err = db.Exec("DELETE FROM window_states WHERE profile_id = ?", profileID)
	if err != nil {
		return fmt.Errorf("error clearing existing window states: %v", err)
	}

	// Insert the new window states
	stmt, err := db.Prepare("INSERT INTO window_states (profile_id, app_name, window_title, x, y, width, height) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("error preparing statement: %v", err)
	}
	defer stmt.Close()

	for _, state := range states {
		_, err = stmt.Exec(
			profileID,
			state.AppName,
			state.WindowTitle,
			state.X,
			state.Y,
			state.Width,
			state.Height,
		)
		if err != nil {
			return fmt.Errorf("error inserting window state: %v", err)
		}
	}

	return nil
}

func loadWindowStates(db *sql.DB, profileName string) ([]WindowState, error) {
	// First get the profile ID
	var profileID int
	err := db.QueryRow("SELECT id FROM profiles WHERE name = ?", profileName).Scan(&profileID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("profile %s not found", profileName)
		}
		return nil, fmt.Errorf("error finding profile: %v", err)
	}

	rows, err := db.Query(
		"SELECT app_name, window_title, x, y, width, height FROM window_states WHERE profile_id = ?",
		profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("error querying window states: %v", err)
	}
	defer rows.Close()

	var states []WindowState
	for rows.Next() {
		var state WindowState
		err := rows.Scan(
			&state.AppName,
			&state.WindowTitle,
			&state.X,
			&state.Y,
			&state.Width,
			&state.Height,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %v", err)
		}
		states = append(states, state)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	return states, nil
}

func getProfiles(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM profiles ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("error querying profiles: %v", err)
	}
	defer rows.Close()

	var profiles []string
	for rows.Next() {
		var name string
		err := rows.Scan(&name)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %v", err)
		}
		profiles = append(profiles, name)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	return profiles, nil
}

func deleteProfile(db *sql.DB, profileName string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error starting transaction: %v", err)
	}

	// First get the profile ID
	var profileID int
	err = tx.QueryRow("SELECT id FROM profiles WHERE name = ?", profileName).Scan(&profileID)
	if err != nil {
		tx.Rollback()
		if err == sql.ErrNoRows {
			return fmt.Errorf("profile %s not found", profileName)
		}
		return fmt.Errorf("error finding profile: %v", err)
	}

	_, err = tx.Exec("DELETE FROM window_states WHERE profile_id = ?", profileID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("error deleting window states: %v", err)
	}

	_, err = tx.Exec("DELETE FROM profiles WHERE id = ?", profileID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("error deleting profile: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("error committing transaction: %v", err)
	}

	return nil
}

// Gets the current window states from macOS using AppleScript
func getCurrentWindowStates() []WindowState {
	// Initialize an empty slice to store window states
	var states []WindowState

	// AppleScript to get information about all visible windows
	script := `
tell application "System Events"
	set appList to application processes whose visible is true
	set windowData to ""
	
	repeat with appProcess in appList
		set appName to name of appProcess as string
		set windowList to windows of appProcess
		
		repeat with theWindow in windowList
			set winTitle to ""
			try
				set winTitle to name of theWindow as string
			end try
			
			set winPos to position of theWindow
			set winSize to size of theWindow
			
			set windowData to windowData & appName & "," & winTitle & "," & (item 1 of winPos as string) & "," & (item 2 of winPos as string) & "," & (item 1 of winSize as string) & "," & (item 2 of winSize as string) & "\n"
		end repeat
	end repeat
	
	return windowData
end tell
`

	// Execute the AppleScript
	cmd := exec.Command("osascript", "-e", script)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Error getting window states: %v", err)
		return states
	}

	// Parse the output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Split(line, ",")
		if len(parts) < 6 {
			continue
		}

		// Parse position and size
		x, _ := strconv.ParseFloat(parts[2], 64)
		y, _ := strconv.ParseFloat(parts[3], 64)
		width, _ := strconv.ParseFloat(parts[4], 64)
		height, _ := strconv.ParseFloat(parts[5], 64)

		states = append(states, WindowState{
			AppName:     parts[0],
			WindowTitle: parts[1],
			X:           x,
			Y:           y,
			Width:       width,
			Height:      height,
		})
	}

	return states
}

// Restores window states using AppleScript
func restoreWindowStates(states []WindowState) {
	for _, state := range states {
		// AppleScript to restore window position and size
		script := fmt.Sprintf(`
tell application "System Events"
	set appList to application processes whose name is "%s"
	if (count of appList) > 0 then
		set appProcess to item 1 of appList
		set windowList to windows of appProcess whose name is "%s"
		if (count of windowList) > 0 then
			set theWindow to item 1 of windowList
			set position of theWindow to {%d, %d}
			set size of theWindow to {%d, %d}
		end if
	end if
end tell
`, state.AppName, state.WindowTitle, int(state.X), int(state.Y), int(state.Width), int(state.Height))

		// Execute the AppleScript
		cmd := exec.Command("osascript", "-e", script)
		err := cmd.Run()
		if err != nil {
			log.Printf("Error restoring window state for %s - %s: %v", state.AppName, state.WindowTitle, err)
		}
	}
}

func main() {
	// Initialize the database
	db := initDB()
	defer db.Close()

	// Initialize the Fyne app
	myApp := app.New()
	myWindow := myApp.NewWindow("Wisa - Window State Manager")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Create profile selection dropdown with option to create new profiles
	profiles, err := getProfiles(db)
	if err != nil {
		log.Printf("Error getting profiles: %v", err)
		profiles = []string{}
	}

	// Add "Create New Profile..." option
	profileOptions := append([]string{"Create New Profile..."}, profiles...)

	var selectedProfile string
	profileSelect := widget.NewSelect(profileOptions, nil)
	profileSelect.SetSelected("Create New Profile...")

	// Track if we're in "create new" mode
	var isCreatingNew bool = true

	// Create input field for new profile name with fixed width
	profileNameEntry := widget.NewEntry()
	profileNameEntry.SetPlaceHolder("New Profile Name")

	// Status label
	statusLabel := widget.NewLabel("")

	// Window states display
	statesTextArea := widget.NewMultiLineEntry()
	statesTextArea.Disable()
	statesTextArea.SetText("Select a profile to see saved window states")
	statesTextArea.Wrapping = fyne.TextWrapWord

	// Function to refresh the profile list
	refreshProfiles := func() {
		newProfiles, err := getProfiles(db)
		if err != nil {
			log.Printf("Error getting profiles: %v", err)
			return
		}

		// Always add "Create New Profile..." option at the top
		profileOptions := append([]string{"Create New Profile..."}, newProfiles...)
		profileSelect.Options = profileOptions

		// Try to keep the previous selection if it exists
		if selectedProfile != "" && selectedProfile != "Create New Profile..." {
			// Check if the previously selected profile still exists
			var found bool
			for _, profile := range newProfiles {
				if profile == selectedProfile {
					found = true
					profileSelect.SetSelected(selectedProfile)
					break
				}
			}

			if !found {
				// Previously selected profile no longer exists
				profileSelect.SetSelected("Create New Profile...")
				isCreatingNew = true
				profileNameEntry.Enable()
				profileNameEntry.SetText("")
			}
		} else {
			// Default to "Create New Profile..." if no selection or was already on create new
			profileSelect.SetSelected("Create New Profile...")
			isCreatingNew = true
			profileNameEntry.Enable()
		}

		profileSelect.Refresh()
	}

	// Function to display window states
	displayWindowStates := func(states []WindowState) {
		if len(states) == 0 {
			statesTextArea.SetText("No window states found for this profile")
			return
		}

		text := fmt.Sprintf("Profile has %d window states:\n\n", len(states))
		for i, state := range states {
			text += fmt.Sprintf("%d. %s - %s\n   Position: (%.0f, %.0f) Size: %.0f x %.0f\n\n",
				i+1, state.AppName, state.WindowTitle,
				state.X, state.Y, state.Width, state.Height)
		}
		statesTextArea.SetText(text)
	}

	// Update the profile selection handler
	profileSelect.OnChanged = func(selected string) {
		if selected == "" {
			statesTextArea.SetText("Select a profile to see saved window states")
			return
		}

		selectedProfile = selected

		if selected == "Create New Profile..." {
			isCreatingNew = true
			profileNameEntry.Enable()
			profileNameEntry.SetText("")
			statesTextArea.SetText("Enter a name for your new profile")
			return
		}

		// Not creating a new profile, so disable profile name entry
		isCreatingNew = false
		profileNameEntry.Disable()
		profileNameEntry.SetText(selected)

		states, err := loadWindowStates(db, selected)
		if err != nil {
			statesTextArea.SetText(fmt.Sprintf("Error: %v", err))
			return
		}

		displayWindowStates(states)
	}

	// Create buttons
	saveButton := widget.NewButton("Save Current Window States", func() {
		var profileName string

		if isCreatingNew {
			// Using the text from the entry for a new profile
			profileName = profileNameEntry.Text
			if profileName == "" {
				statusLabel.SetText("Please enter a profile name")
				return
			}
		} else {
			// Using the selected existing profile
			profileName = selectedProfile
			// Double check it's not the "Create New" option
			if profileName == "Create New Profile..." {
				statusLabel.SetText("Please select a valid profile or create a new one")
				return
			}
		}

		statusLabel.SetText("Saving window states...")
		states := getCurrentWindowStates()
		err := saveWindowStates(db, profileName, states)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error saving window states: %v", err))
			return
		}

		statusLabel.SetText(fmt.Sprintf("Saved %d window states to profile '%s'", len(states), profileName))

		if isCreatingNew {
			profileNameEntry.SetText("")
		}

		refreshProfiles()

		// Auto-select the newly created/updated profile in the dropdown
		// We need to find it in the updated options list which now includes the "Create New" option
		for _, option := range profileSelect.Options {
			if option == profileName {
				profileSelect.SetSelected(profileName)
				break
			}
		}

		displayWindowStates(states)
	})

	loadButton := widget.NewButton("Load Selected Profile", func() {
		profileName := profileSelect.Selected
		if profileName == "" {
			statusLabel.SetText("Please select a profile")
			return
		}

		// Check if we're in "create new" mode - can't load a profile that doesn't exist yet
		if profileName == "Create New Profile..." {
			statusLabel.SetText("Please select an existing profile to load")
			return
		}

		statusLabel.SetText("Loading window states...")
		states, err := loadWindowStates(db, profileName)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error loading window states: %v", err))
			return
		}

		if len(states) == 0 {
			statusLabel.SetText(fmt.Sprintf("No window states found for profile '%s'", profileName))
			return
		}

		statusLabel.SetText("Restoring window states...")
		restoreWindowStates(states)
		statusLabel.SetText(fmt.Sprintf("Restored %d window states from profile '%s'", len(states), profileName))

		// Start a timer to clear the status message after 3 seconds
		go func() {
			time.Sleep(3 * time.Second)
			statusLabel.SetText("")
		}()
	})

	deleteButton := widget.NewButton("Delete Selected Profile", func() {
		profileName := profileSelect.Selected
		if profileName == "" {
			statusLabel.SetText("Please select a profile")
			return
		}

		// Check if we're in "create new" mode - can't delete a profile that doesn't exist yet
		if profileName == "Create New Profile..." {
			statusLabel.SetText("Please select an existing profile to delete")
			return
		}

		err := deleteProfile(db, profileName)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error deleting profile: %v", err))
			return
		}

		statusLabel.SetText(fmt.Sprintf("Deleted profile '%s'", profileName))
		statesTextArea.SetText("Select a profile to see saved window states")
		refreshProfiles()
	})

	// Create layout with a clearer design for the combo profile selector
	topContent := container.NewVBox(
		widget.NewLabel("Wisa - Window State Manager"),
		widget.NewLabel("Select or Create Profile:"),
		profileSelect,
		// Profile name entry only shows when creating a new profile
		container.New(
			layout.NewFormLayout(),
			widget.NewLabel("Profile Name:"),
			profileNameEntry,
		),
		container.NewHBox(
			saveButton,
			loadButton,
			deleteButton,
		),
	)

	content := container.NewBorder(
		topContent,
		statusLabel,
		nil,
		nil,
		container.NewVScroll(statesTextArea),
	)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}
