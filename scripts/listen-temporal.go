package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lib/pq"
)

// --- Configuration ---
const (
	// CONNECTION CONFIGURATION
	// The full connection URL will be read from this environment variable.
	DBUrlEnv = "DB_URL"

	// LISTEN/TRIGGER CONFIGURATION
	TableName   = "executions_visibility"
	TriggerName = "workflow_data_notifier"
	FuncName    = "notify_table_changes_with_data"
	ChannelName = "workflow_events"
)

// List of fields that should always be displayed in the 'identifiers' block.
var SpecialKeys = []string{
	"workflow_type_name", "workflow_id", "run_id",
	"root_run_id", "root_workflow_id", "parent_run_id", "parent_workflow_id",
}

// --- SQL Statements ---

// SQL function definition to capture OLD and NEW row data as JSON and send it via NOTIFY.
var createFuncSQL = fmt.Sprintf(`
CREATE OR REPLACE FUNCTION %s()
RETURNS TRIGGER AS $$
DECLARE
  old_data JSONB;
  new_data JSONB;
  payload JSON;
BEGIN
  -- Capture OLD data for UPDATE and DELETE operations
  IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
    old_data := row_to_json(OLD)::JSONB;
  ELSE
    old_data := NULL;
  END IF;
  
  -- Capture NEW data for INSERT and UPDATE operations
  IF TG_OP = 'UPDATE' OR TG_OP = 'INSERT' THEN
    new_data := row_to_json(NEW)::JSONB;
  ELSE
    new_data = NULL;
  END IF;
  
  -- Construct the main notification payload
  payload := json_build_object(
    'table', TG_TABLE_NAME,
    'action', TG_OP,
    'timestamp', now(),
    'old', old_data,
    'new', new_data
  );
  
  -- Send the notification to the channel
  PERFORM pg_notify('%s', payload::TEXT);
  
  -- For AFTER triggers, always return the row
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  ELSE
    RETURN NEW;
  END IF;
END;
$$ LANGUAGE plpgsql;`, FuncName, ChannelName)

// SQL statement to attach the trigger function to the target table.
var createTriggerSQL = fmt.Sprintf(`
CREATE TRIGGER %s
AFTER INSERT OR UPDATE OR DELETE ON %s
FOR EACH ROW EXECUTE FUNCTION %s();`, TriggerName, TableName, FuncName)

// SQL statements for cleanup.
var dropTriggerSQL = fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s;", TriggerName, TableName)
var dropFuncSQL = fmt.Sprintf("DROP FUNCTION IF EXISTS %s() CASCADE;", FuncName)

// filterNulls creates a new map containing only non-nil values from the input map.
func filterNulls(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	filtered := make(map[string]interface{})
	for k, v := range data {
		// Nil check handles JSON 'null' values unmarshaled as Go nil
		if v != nil {
			filtered[k] = v
		}
	}
	return filtered
}

func main() {
	// --- Read Connection String from Environment Variable ---
	connStr := os.Getenv(DBUrlEnv)
	if connStr == "" {
		log.Fatalf("Fatal: Database connection string not found. Please set the environment variable: %s", DBUrlEnv)
	}

	// Create a listener connection for asynchronous notifications
	listener := pq.NewListener(connStr, 10*time.Second, 60*time.Second, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			log.Printf("[Listener Error] %v", err)
		}
	})

	// --- 1. Signal Handling for Graceful Exit ---
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// --- 2. Database Setup and Cleanup ---
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// --- CLEANUP DEFER (Runs when the program exits) ---
	defer func() {
		log.Println("\n🧹 Starting database cleanup...")
		if _, err := db.Exec(dropTriggerSQL); err != nil {
			log.Printf("Failed to drop trigger: %v", err)
		} else {
			log.Printf("✅ Dropped Trigger: %s", TriggerName)
		}
		if _, err := db.Exec(dropFuncSQL); err != nil {
			log.Printf("Failed to drop function: %v", err)
		} else {
			log.Printf("✅ Dropped Function: %s", FuncName)
		}
	}()

	// --- SETUP (Create the objects) ---
	log.Println("⚙️  Setting up database objects...")
	if _, err := db.Exec(createFuncSQL); err != nil {
		log.Fatalf("Failed to create function: %v", err)
	} else {
		log.Printf("✅ Created Function: %s", FuncName)
	}
	if _, err := db.Exec(createTriggerSQL); err != nil {
		log.Fatalf("Failed to create trigger: %v", err)
	} else {
		log.Printf("✅ Created Trigger: %s on %s", TriggerName, TableName)
	}

	// --- 3. Start Listening ---
	if err := listener.Listen(ChannelName); err != nil {
		log.Fatalf("Failed to listen on channel %s: %v", ChannelName, err)
	}
	log.Printf("👂 Listening live on channel '%s'. Press Ctrl+C to stop...", ChannelName)

	// --- 4. Main Loop ---
	for {
		select {
		case <-sigChan:
			// Received interrupt signal, exit loop
			return
		case notification := <-listener.Notify:
			// Received a notification, process the payload
			if notification != nil && notification.Extra != "" {
				log.Printf("\n🔔 --- CHANGE DETECTED [%s] ---", time.Now().Format("15:04:05"))

				var rawPayload map[string]interface{}
				if err := json.Unmarshal([]byte(notification.Extra), &rawPayload); err != nil {
					log.Printf("Error decoding payload: %v", err)
					continue
				}

				// --- Core Logic: Extract and Diff Changes ---

				action, _ := rawPayload["action"].(string)
				table, _ := rawPayload["table"].(string)
				oldMap, _ := rawPayload["old"].(map[string]interface{})
				newMap, _ := rawPayload["new"].(map[string]interface{})

				// 1. Prepare the final output structure
				output := map[string]interface{}{
					"table":   table,
					"action":  action,
					"changes": map[string]interface{}{},
				}

				// 2. Extract Special Identifiers (always visible)
				identifiers := make(map[string]interface{})
				sourceMap := newMap // Primary source is NEW data
				if sourceMap == nil {
					sourceMap = oldMap // If DELETE, source is OLD data
				}

				for _, key := range SpecialKeys {
					if val, ok := sourceMap[key]; ok {
						identifiers[key] = val
					}
				}
				output["identifiers"] = identifiers

				// 3. Process changes based on action
				diff := make(map[string]map[string]interface{})

				switch action {
				case "INSERT":
					// For INSERT, show the NEW row data, filtered to hide nulls.
					output["changes"] = map[string]interface{}{
						"NEW_ROW": filterNulls(newMap),
					}

				case "DELETE":
					// For DELETE, show the entire OLD row (all fields are 'deleted').
					output["changes"] = map[string]interface{}{
						"OLD_ROW": oldMap,
					}

				case "UPDATE":
					// For UPDATE, compare old and new fields to show the difference.
					if oldMap != nil && newMap != nil {
						for key, newValue := range newMap {
							oldValue := oldMap[key]

							// Use JSON Marshalling for robust comparison across types
							oldBytes, _ := json.Marshal(oldValue)
							newBytes, _ := json.Marshal(newValue)

							if string(oldBytes) != string(newBytes) {
								diff[key] = map[string]interface{}{
									"old": oldValue,
									"new": newValue,
								}
							}
						}
						output["changes"] = diff
					}
				}

				// Pretty-print the structured output
				if data, err := json.MarshalIndent(output, "", "  "); err == nil {
					fmt.Println(string(data))
				} else {
					fmt.Printf("Error marshaling diff output: %v\n", err)
				}
				log.Println("----------------------------------\n")
			}
		case <-time.After(10 * time.Second):
			// Keep-alive/timeout
		}
	}
}
