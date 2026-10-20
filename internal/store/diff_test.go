package store

import (
	"reflect"
	"testing"
)

func TestDiff(t *testing.T) {
	got := diff(map[string]any{"same": "x", "gone": 1.0, "owners": []any{"C&A, LLC"}},
		map[string]any{"same": "x", "added": "y", "owners": []any{"C&A, LLC", "PRIMARY"}})
	want := []FieldChange{
		{Field: "added", To: "y"},
		{Field: "gone", From: "1"},
		{Field: "owners", From: `["C&A, LLC"]`, To: `["C&A, LLC","PRIMARY"]`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diff = %+v\nwant %+v", got, want)
	}
}
