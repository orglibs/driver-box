package internal

import "fmt"

func pointReadWindow(values []uint16, address uint16, point *Point) ([]uint16, error) {
	start := int(point.Address) - int(address)
	end := start + int(point.Quantity)
	if start < 0 || point.Quantity == 0 || end > len(values) || uint32(point.Address)+uint32(point.Quantity) > 65536 {
		return nil, fmt.Errorf("invalid register window: address=%d quantity=%d groupAddress=%d received=%d", point.Address, point.Quantity, address, len(values))
	}
	return values[start:end], nil
}
