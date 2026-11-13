package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"go.uber.org/zap"
)

func selectionCache(t *testing.T) *cache {
	t.Helper()
	previous := logger.Logger
	logger.Logger = zap.NewNop()
	t.Cleanup(func() { logger.Logger = previous })
	c := &cache{mutex: &sync.RWMutex{}, plugins: map[string]cachePlugin{"test": {}}, models: map[string]cacheModel{}, devices: map[string]cacheDevice{}}
	if err := c.AddModel("test", config.Model{Name: "model", ModelID: "cloud-model", DevicePoints: []config.Point{{"name": "first"}, {"name": "second"}}}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []config.Device{
		{ID: "all", ModelName: "model"},
		{ID: "selected", ModelName: "model", PointSelection: &config.PointSelection{Mode: "selected", Names: []string{"first"}}},
		{ID: "empty", ModelName: "model", PointSelection: &config.PointSelection{Mode: "selected", Names: []string{}}},
	} {
		compiled, err := compileDevice(d, c.models["model"].Model)
		if err != nil {
			t.Fatal(err)
		}
		c.devices[d.ID] = compiled
	}
	return c
}

func TestPointSelectionCacheAndModelUpdate(t *testing.T) {
	c := selectionCache(t)
	for id, want := range map[string]int{"all": 2, "selected": 1, "empty": 0} {
		points, ok := c.GetDevicePoints(id)
		if !ok || points == nil || len(points) != want {
			t.Fatalf("%s points=%v ok=%v", id, points, ok)
		}
	}
	if _, ok := c.GetDevicePoints("unknown"); ok {
		t.Fatal("unknown device accepted")
	}
	if _, ok := c.GetPointByDevice("selected", "second"); ok {
		t.Fatal("excluded point is accessible")
	}
	if _, ok := c.GetPointByModel("model", "second"); !ok {
		t.Fatal("shared model was filtered")
	}
	device, _ := c.GetDevice("selected")
	device.PointSelection.Names[0] = "second"
	if _, ok := c.GetPointByDevice("selected", "first"); !ok {
		t.Fatal("caller changed runtime selection")
	}
	model := c.models["model"].Model
	model.DevicePoints = append(append([]config.Point{}, model.DevicePoints...), config.Point{"name": "new"})
	if err := c.AddModel("test", model); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"all": 3, "selected": 1, "empty": 0} {
		points, _ := c.GetDevicePoints(id)
		if len(points) != want {
			t.Fatalf("%s growth points=%v", id, points)
		}
	}
	bad := model
	bad.DevicePoints = []config.Point{{"name": "second"}}
	if err := c.AddModel("test", bad); err == nil {
		t.Fatal("removing selected name accepted")
	}
	if points, _ := c.GetDevicePoints("all"); len(points) != 3 {
		t.Fatal("failed update partially changed model")
	}
	device, _ = c.GetDevice("selected")
	device.PointSelection.Names = []string{"missing"}
	if err := c.AddOrUpdateDevice(device); err == nil {
		t.Fatal("invalid device update accepted")
	}
	if _, ok := c.GetPointByDevice("selected", "first"); !ok {
		t.Fatal("failed update changed selection")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.GetDevicePoints("selected")
				c.GetPointByDevice("empty", "first")
				c.GetDevice("selected")
			}
		}()
	}
	for i := 0; i < 50; i++ {
		if err := c.AddModel("test", model); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestPointSelectionConfigParsingAndPersistence(t *testing.T) {
	c := selectionCache(t)
	file := filepath.Join(t.TempDir(), "config.json")
	device, _ := c.GetDevice("empty")
	model := config.DeviceModel{Model: c.models["model"].Model, Devices: []config.Device{device}}
	data, err := json.Marshal(config.DeviceConfig{PluginName: "test", DeviceModels: []config.DeviceModel{model}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := parseConfigFromFile(file)
	if err != nil {
		t.Fatal(err)
	}
	points, err := loaded.DeviceModels[0].PointsFor(loaded.DeviceModels[0].Devices[0])
	if err != nil || points == nil || len(points) != 0 {
		t.Fatal("zero selection did not survive reload", err)
	}
	model.Devices[0].PointSelection.Names = []string{"missing"}
	data, _ = json.Marshal(config.DeviceConfig{DeviceModels: []config.DeviceModel{model}})
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseConfigFromFile(file); err == nil {
		t.Fatal("startup accepted unknown selected point")
	}
}
