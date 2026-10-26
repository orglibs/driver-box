package internal

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox"
	"golang.org/x/sys/unix"
)

// A pseudo-terminal exercises the actual serial route and RTU CRC framing.
// This is an automated simulation, not a physical serial device test.
func TestModbusStringRTUSerial(t *testing.T) {
	if os.Getenv("DRIVERBOX_STRING_RTU_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestModbusStringRTUSerial$", "-test.timeout=20s")
		cmd.Env = append(os.Environ(), "DRIVERBOX_STRING_RTU_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("RTU serial simulation: %v\n%s", err, output)
		}
		return
	}
	recorder, points := initStringFramework(t)
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0600)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "rtu-master")
	defer master.Close()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/dev/pts/%d", number)
	// Keep the slave open while the serial client is being constructed.
	anchor, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	conf := &ConnectionConfig{Mode: "rtu", Address: path, BaudRate: 19200, DataBits: 8, StopBits: 1, Timeout: 1000, MinInterval: 1}
	c, err := newConnector(&Plugin{}, conf)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	slave := &stringSlave{}
	slave.set(27, []uint16{0xe4b8, 0xade6, 0x9687})
	done := make(chan struct{})
	go func() { defer close(done); serveStringSlave(t, master, true, slave) }()
	defer func() { _ = master.Close(); <-done }()
	ext, err := convToPointExtend(points[6])
	if err != nil {
		t.Fatal(err)
	}
	ext.DeviceId = "string-A"
	group := &pointGroup{UnitID: 1, Address: 27, Quantity: 16, RegisterType: HoldingRegister, Points: []*Point{ext}}
	if err := c.sendReadCommand(group); err != nil {
		t.Fatal(err)
	}
	got, err := driverbox.Shadow().GetDevicePoint("string-A", "esn")
	if err != nil || got != "中文" {
		t.Fatalf("serial Shadow=%q,%v", got, err)
	}
	_, batches := recorder.take()
	if len(batches) != 1 || len(batches[0].Values) != 1 || batches[0].Values[0].Value != "中文" {
		t.Fatalf("serial ExportTo %+v", batches)
	}
	requests := slave.take()
	if len(requests) != 1 || requests[0].address != 27 || requests[0].quantity != 16 {
		t.Fatalf("serial requests %+v", requests)
	}
}
