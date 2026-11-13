package config

import "testing"

func TestPointReadWrite(t *testing.T) {
	for _, access := range []ReadWrite{ReadWrite_R, ReadWrite_W, ReadWrite_RW} {
		for _, raw := range []interface{}{string(access), access} {
			if got := (Point{"readWrite": raw}).ReadWrite(); got != access {
				t.Errorf("readWrite=%T(%v): got %q, want %q", raw, raw, got, access)
			}
		}
	}
	for _, raw := range []interface{}{nil, true, 1, 1.5, []string{"W"}, map[string]string{"value": "W"}, (*string)(nil)} {
		if got := (Point{"readWrite": raw}).ReadWrite(); got != "" {
			t.Errorf("readWrite=%T(%v): got %q, want empty for invalid type", raw, raw, got)
		}
	}
	for _, point := range []Point{nil, {}} {
		if got := point.ReadWrite(); got != "" {
			t.Errorf("missing readWrite = %q, want empty", got)
		}
	}
	// String values retain their previous semantics; validation rejects unknown modes.
	for _, tt := range []struct {
		raw  interface{}
		want ReadWrite
	}{{"", ""}, {"w", "w"}, {"invalid", "invalid"}, {ReadWrite("invalid"), "invalid"}} {
		if got := (Point{"readWrite": tt.raw}).ReadWrite(); got != tt.want {
			t.Errorf("readWrite=%v: got %q, want %q", tt.raw, got, tt.want)
		}
	}
}
