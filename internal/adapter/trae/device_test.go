package trae

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoginDevicePersistsAndDoesNotResetCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "device.json")
	first, err := persistentLoginDevice(p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := persistentLoginDevice(p)
	if err != nil || first != second || len(first.DeviceID) != 15 || len(first.MachineID) != 64 {
		t.Fatal("installation identity changed")
	}
	if err := os.WriteFile(p, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := persistentLoginDevice(p); err == nil {
		t.Fatal("corrupt identity was reset")
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "invalid" {
		t.Fatal("corrupt identity was overwritten")
	}
}

func TestDesktopLoginDeviceReadsOnlyExistingIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(p, []byte(`{"device_id":"existing-device","telemetry.machineId":"existing-machine","other":"ignore"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := desktopLoginDevice(p)
	if err != nil || d.DeviceID != "existing-device" || d.MachineID != "existing-machine" {
		t.Fatal("desktop identity not reused")
	}
}
