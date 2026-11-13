package internal

import (
	"fmt"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

// Preserve the original span-based batching by default. An explicit gap limit
// can reduce extra reads; optional write-only barriers split groups when enabled.
func mergeReadWindow(group *pointGroup, point *Point, conf *ConnectionConfig, writeOnly []*Point) (uint16, uint16, bool) {
	start, end := uint32(group.Address), uint32(group.Address)+uint32(group.Quantity)
	pointStart, pointEnd := uint32(point.Address), uint32(point.Address)+uint32(point.Quantity)
	var gap uint32
	if pointStart > end {
		gap = pointStart - end
	} else if start > pointEnd {
		gap = start - pointEnd
	}
	limit := readGroupLimit(conf, point.RegisterType)
	maxGap := limit
	if conf.MaxReadGap != nil {
		maxGap = uint32(*conf.MaxReadGap)
	}
	if gap > maxGap {
		return 0, 0, false
	}
	if pointStart < start {
		start = pointStart
	}
	if pointEnd > end {
		end = pointEnd
	}
	if end > 65536 || end-start > limit {
		return 0, 0, false
	}
	if overlappingWriteOnlyPoint(start, end, point.RegisterType, writeOnly) != nil {
		return 0, 0, false
	}
	return uint16(start), uint16(end - start), true
}

func overlappingWriteOnlyPoint(start, end uint32, table primaryTable, writeOnly []*Point) *Point {
	for _, blocked := range writeOnly {
		if blocked.RegisterType == table && uint32(blocked.Address) < end && uint32(blocked.Address)+uint32(blocked.Quantity) > start {
			return blocked
		}
	}
	return nil
}

func writeOnlyReadBarriers(points []config.Point) ([]*Point, error) {
	var barriers []*Point
	for _, point := range points {
		if point.ReadWrite() != config.ReadWrite_W {
			continue
		}
		ext, err := writeOnlyReadRange(point)
		if err != nil {
			return nil, fmt.Errorf("write-only point %v read barrier: %w", point["name"], err)
		}
		barriers = append(barriers, ext)
	}
	return barriers, nil
}

// A disabled point's write-value options do not change its physical range.
// Parse only range metadata; never silently omit a range we cannot determine.
func writeOnlyReadRange(point config.Point) (*Point, error) {
	rangePoint := config.Point{"readWrite": "R", "valueType": "int"}
	for _, field := range []string{"name", "primaryTable", "startAddress", "rawType", "quantity"} {
		if value, ok := point[field]; ok {
			rangePoint[field] = value
		}
	}
	if isStringPoint(rangePoint) {
		rangePoint["valueType"] = "string"
	}
	// The legacy numeric address parser narrows some inputs to uint16. Reject
	// out-of-range inputs before narrowing while retaining its address notation.
	if _, err := stringPointAddress(rangePoint["startAddress"]); err != nil {
		return nil, err
	}
	ext, err := convToPointExtend(rangePoint)
	if err != nil {
		return nil, err
	}
	if uint32(ext.Address)+uint32(ext.Quantity) > 65536 {
		return nil, fmt.Errorf("startAddress + quantity exceeds 65536")
	}
	ext.Point = point
	return ext, nil
}

func pointReadWindow(values []uint16, address uint16, point *Point) ([]uint16, error) {
	start := int(point.Address) - int(address)
	end := start + int(point.Quantity)
	if start < 0 || point.Quantity == 0 || end > len(values) || uint32(point.Address)+uint32(point.Quantity) > 65536 {
		return nil, fmt.Errorf("invalid register window: address=%d quantity=%d groupAddress=%d received=%d", point.Address, point.Quantity, address, len(values))
	}
	return values[start:end], nil
}
