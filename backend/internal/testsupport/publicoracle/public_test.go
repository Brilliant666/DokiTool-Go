package publicoracle

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDeclaredDifferencesMatchFrozenPHP(t *testing.T) {
	oracle, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(oracle.changes) != 10 {
		t.Fatal("review the exact difference inventory before extending this migration exception")
	}
	for _, fixture := range []string{DouyinFixture, IntegerFixture} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", fixture))
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err := decode(data, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, value := range manifest["cases"].([]any) {
			entry := value.(map[string]any)
			for _, view := range []string{"normal", "debug"} {
				frozen := entry["expected"].(map[string]any)[view]
				before, _ := json.Marshal(frozen)
				if _, err := oracle.Expected(fixture, entry["id"].(string), view, frozen); err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(frozen)
				if !bytes.Equal(before, after) {
					t.Fatal("oracle modified the frozen PHP input")
				}
			}
		}
	}
	if err := oracle.RequireConsumed(""); err != nil {
		t.Fatal(err)
	}
}

func oneChange() difference {
	return difference{Fixture: IntegerFixture, CaseID: "size", View: "normal", Path: "/source/size_bytes", Operation: "remove", Before: json.Number("9007199254740993"), Reason: "Exact integer boundary regression"}
}

func loadChanges(t *testing.T, changes []difference) (*Oracle, error) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"format_version": 1, "differences": changes})
	if err != nil {
		t.Fatal(err)
	}
	return load(data)
}

func TestOracleRejectsInvalidDeclarations(t *testing.T) {
	for _, name := range []string{"duplicate", "unknown-fixture", "wrong-view", "no-reason", "no-before", "missing-after", "unexpected-after", "unknown-operation"} {
		t.Run(name, func(t *testing.T) {
			change := oneChange()
			changes := []difference{change}
			switch name {
			case "duplicate":
				changes = append(changes, change)
			case "unknown-fixture":
				changes[0].Fixture = "unreviewed.json"
			case "wrong-view":
				changes[0].View = "formatter"
			case "no-reason":
				changes[0].Reason = ""
			case "no-before":
				changes[0].Before = nil
			case "missing-after":
				changes[0].Operation = "replace"
			case "unexpected-after":
				changes[0].After = "ignored"
			case "unknown-operation":
				changes[0].Operation = "ignore"
			}
			if _, err := loadChanges(t, changes); err == nil {
				t.Fatal("invalid declaration was accepted")
			}
		})
	}
}

func TestOracleExactBeforeAndConsumption(t *testing.T) {
	for _, before := range []any{nil, json.Number("9007199254740992"), "9007199254740993"} {
		oracle, err := loadChanges(t, []difference{oneChange()})
		if err != nil {
			t.Fatal(err)
		}
		source := map[string]any{}
		if before != nil {
			source["size_bytes"] = before
		}
		if _, err := oracle.Expected(IntegerFixture, "size", "normal", map[string]any{"source": source}); err == nil {
			t.Fatalf("missing, rounded or differently typed before value accepted: %#v", before)
		}
		if err := oracle.RequireConsumed(IntegerFixture); err == nil {
			t.Fatal("rejected difference was marked consumed")
		}
	}
	oracle, err := loadChanges(t, []difference{oneChange()})
	if err != nil {
		t.Fatal(err)
	}
	frozen := map[string]any{"source": map[string]any{"size_bytes": json.Number("9007199254740993"), "id": "stable-id"}}
	unchanged, err := oracle.Expected(IntegerFixture, "different-case", "normal", frozen)
	if err != nil || !reflect.DeepEqual(unchanged, frozen) {
		t.Fatal("undeclared case was changed")
	}
	if err := oracle.RequireConsumed(IntegerFixture); err == nil {
		t.Fatal("unused declaration was accepted")
	}
	expected, err := oracle.Expected(IntegerFixture, "size", "normal", frozen)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := expected["source"].(map[string]any)["size_bytes"]; present || expected["source"].(map[string]any)["id"] != "stable-id" {
		t.Fatal("expected response did not apply only the declared removal")
	}
	if _, present := frozen["source"].(map[string]any)["size_bytes"]; !present {
		t.Fatal("frozen response was mutated")
	}
	if err := oracle.RequireConsumed(IntegerFixture); err != nil {
		t.Fatal(err)
	}
}
