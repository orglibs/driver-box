package cache

import (
	"testing"

	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"go.uber.org/zap"
)

// Invalid new offset configuration must reach plugin validation without a
// panic in the cache's legacy scale type assertion.
func TestCheckPointOffset(t *testing.T) {
	previous := logger.Logger
	logger.Logger = zap.NewNop()
	defer func() { logger.Logger = previous }()
	for _, scale := range []interface{}{nil, "0.1", true, []int{1}, .1, 1} {
		point := config.Point{"name": "temperature", "description": "temperature", "valueType": "float", "readWrite": "RW", "scale": scale, "offset": -40}
		checkPoint(&config.Model{Name: "offset"}, &point)
	}
}
