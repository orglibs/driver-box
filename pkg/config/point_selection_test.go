package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPointSelection(t *testing.T) {
	points := []Point{{"name": "common.totalPower"}, {"name": "gun1.voltage"}, {"name": "later"}}
	for _, tc := range []struct {
		selection *PointSelection
		want      []string
	}{
		{nil, []string{"common.totalPower", "gun1.voltage", "later"}},
		{&PointSelection{Mode: "all"}, []string{"common.totalPower", "gun1.voltage", "later"}},
		{&PointSelection{Mode: "selected", Names: []string{"gun1.voltage", "common.totalPower"}}, []string{"common.totalPower", "gun1.voltage"}},
		{&PointSelection{Mode: "selected", Names: []string{}}, []string{}},
	} {
		device := Device{ID: "a", PointSelection: tc.selection}
		raw, err := json.Marshal(device)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip Device
		if err := json.Unmarshal(raw, &roundTrip); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(device.PointSelection, roundTrip.PointSelection) {
			t.Fatalf("selection changed during round trip: %s", raw)
		}
		enabled, err := ResolvePointSelection(points, roundTrip.PointSelection)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, point := range enabled {
			got = append(got, point.Name())
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("points=%v want=%v", got, tc.want)
		}
	}
}

func TestPointSelectionInvalid(t *testing.T) {
	points := []Point{{"name": "ok"}}
	for _, raw := range []string{`null`, `false`, `[]`, `{}`, `{"mode":"bad"}`, `{"mode":"selected"}`, `{"mode":"selected","names":null}`, `{"mode":"selected","names":[1]}`, `{"mode":"selected","names":[null]}`, `{"mode":"selected","names":[" "]}`, `{"mode":"selected","names":["missing"]}`, `{"mode":"selected","names":["ok","ok"]}`, `{"mode":"all","names":["ok"]}`} {
		t.Run(raw, func(t *testing.T) {
			var device Device
			if err := json.Unmarshal([]byte(`{"id":"a","pointSelection":`+raw+`}`), &device); err != nil {
				return
			}
			if _, err := ResolvePointSelection(points, device.PointSelection); err == nil {
				t.Fatal("invalid selection accepted")
			}
		})
	}
	if _, err := ResolvePointSelection([]Point{{"name": "ok"}, {"name": "ok"}}, &PointSelection{Mode: "selected", Names: []string{"ok"}}); err == nil {
		t.Fatal("ambiguous selection accepted")
	}
}

func TestPointSelectionDeviceJSONPartialUpdate(t *testing.T) {
	// A distinct type has no custom UnmarshalJSON method and retains the legacy
	// encoding/json behavior for existing Device fields.
	type legacyDevice Device
	newDevice := func(selected bool) Device {
		d := Device{ID: "a", Description: "existing", ModelName: "model", PluginName: "modbus",
			ConnectionKey: "bus", DriverKey: "driver", Properties: map[string]string{"unitID": "1", "area": "west"}}
		if selected {
			d.PointSelection = &PointSelection{Mode: "selected", Names: []string{"first"}}
		}
		return d
	}
	for _, selected := range []bool{false, true} {
		scope := "all"
		if selected {
			scope = "selected"
		}
		for _, raw := range []string{
			`{"id":"b"}`, `{}`, `null`, `{"description":""}`, `{"description":null}`,
			`{"properties":{"unitID":"2"}}`, `{"properties":null}`,
			`{"modelName":"ignored","pluginName":"ignored"}`,
		} {
			t.Run(scope+"/"+raw, func(t *testing.T) {
				got := newDevice(selected)
				want := legacyDevice(newDevice(selected))
				if err := json.Unmarshal([]byte(raw), &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(raw), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, Device(want)) {
					t.Fatalf("partial update changed legacy behavior:\ngot  %#v\nwant %#v", got, Device(want))
				}
			})
		}
	}
}

func TestPointSelectionDeviceJSONReplacesExplicitSelection(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  *PointSelection
		valid bool
	}{
		{`{"pointSelection":{"mode":"all"}}`, &PointSelection{Mode: "all"}, true},
		{`{"pointSelection":{"mode":"selected","names":[]}}`, &PointSelection{Mode: "selected", Names: []string{}}, true},
		{`{"pointSelection":{"mode":"selected"}}`, &PointSelection{Mode: "selected"}, false},
		{`{"pointSelection":{"mode":"selected","names":null}}`, &PointSelection{Mode: "selected"}, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			previous := &PointSelection{Mode: "selected", Names: []string{"first"}}
			device := Device{ID: "a", Description: "existing", ConnectionKey: "bus", PointSelection: previous}
			if err := json.Unmarshal([]byte(tc.raw), &device); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(device.PointSelection, tc.want) || device.ID != "a" || device.Description != "existing" || device.ConnectionKey != "bus" {
				t.Fatalf("unexpected partial selection update: %#v", device)
			}
			if !reflect.DeepEqual(previous, &PointSelection{Mode: "selected", Names: []string{"first"}}) {
				t.Fatal("updating selection mutated the previous selection object")
			}
			_, err := ResolvePointSelection([]Point{{"name": "first"}}, device.PointSelection)
			if (err == nil) != tc.valid {
				t.Fatalf("selection validation changed: %v", err)
			}
		})
	}
}

func TestPointSelectionDeviceJSONRejectsInvalidObject(t *testing.T) {
	for _, raw := range []string{`{"id":"b","pointSelection":null}`, `{"PointSelection":null}`, `{"pointSelection":false}`, `{"pointSelection":{"mode":"selected","names":[true]}}`} {
		device := Device{ID: "a", PointSelection: &PointSelection{Mode: "selected", Names: []string{"first"}}}
		if err := json.Unmarshal([]byte(raw), &device); err == nil {
			t.Fatalf("invalid selection accepted: %s", raw)
		}
		if device.ID != "a" || !reflect.DeepEqual(device.PointSelection, &PointSelection{Mode: "selected", Names: []string{"first"}}) {
			t.Fatalf("failed selection decode changed the existing device: %#v", device)
		}
	}
}
