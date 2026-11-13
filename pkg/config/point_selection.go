package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PointSelection restricts a device to stable model point names. A selected []
// is intentionally different from an omitted selection (all model points).
type PointSelection struct {
	Mode  string   `json:"mode"`
	Names []string `json:"names"`
}

// ResolvePointSelection validates a selection and preserves model point order.
// The returned points refer to the original model definitions; callers must not
// mutate them. No selection and mode all retain the legacy complete point list.
func ResolvePointSelection(points []Point, selection *PointSelection) ([]Point, error) {
	if selection == nil {
		return points, nil
	}
	if selection.Mode == "all" {
		if len(selection.Names) != 0 {
			return nil, fmt.Errorf("pointSelection mode all must not contain names")
		}
		return points, nil
	}
	if selection.Mode != "selected" {
		return nil, fmt.Errorf("pointSelection mode must be all or selected")
	}
	if selection.Names == nil {
		return nil, fmt.Errorf("pointSelection mode selected requires names array; use [] for zero points")
	}
	available := make(map[string]int, len(points))
	for _, point := range points {
		available[point.Name()]++
	}
	selected := make(map[string]bool, len(selection.Names))
	for _, name := range selection.Names {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("pointSelection names must be non-empty strings")
		}
		if selected[name] {
			return nil, fmt.Errorf("pointSelection duplicate point name %q", name)
		}
		if available[name] != 1 {
			return nil, fmt.Errorf("pointSelection unknown or ambiguous point name %q", name)
		}
		selected[name] = true
	}
	enabled := make([]Point, 0, len(selected))
	for _, point := range points {
		if selected[point.Name()] {
			enabled = append(enabled, point)
		}
	}
	return enabled, nil
}

// PointsFor is usable by plugins without requiring an initialized global cache.
func (m Model) PointsFor(device Device) ([]Point, error) {
	points, err := ResolvePointSelection(m.DevicePoints, device.PointSelection)
	if err != nil {
		return nil, fmt.Errorf("model %q device %q: %w", m.Name, device.ID, err)
	}
	return points, nil
}

// ValidatePointSelections is also available to applications for preflight checks.
func (c DeviceConfig) ValidatePointSelections() error {
	for _, model := range c.DeviceModels {
		for _, device := range model.Devices {
			if _, err := model.PointsFor(device); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Device) UnmarshalJSON(data []byte) error {
	type plain Device
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	// Preserve encoding/json's merge behavior for an existing Device, including
	// omitted fields, map entries and runtime metadata excluded from JSON.
	value := plain(*d)
	for key, raw := range fields {
		if !strings.EqualFold(key, "pointSelection") {
			continue
		}
		if strings.TrimSpace(string(raw)) == "null" {
			return fmt.Errorf("pointSelection must be an object; omit it for all points")
		}
		// Explicit selections replace the complete selection object. Do not
		// inherit old names and accidentally accept selected without names.
		value.PointSelection = nil
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*d = Device(value)
	return nil
}
