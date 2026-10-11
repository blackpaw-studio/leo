package push

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/devices"
)

func pairWithPush(t *testing.T, store *devices.Store, name, grant string) devices.Device {
	t.Helper()
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	dev, _, err := store.Pair(code, name)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := store.SetPush(dev.ID, devices.PushRegistration{Grant: grant, Key: key, RelayURL: "https://relay.example"}); err != nil {
		t.Fatal(err)
	}
	return dev
}

func TestStoreRegistryListsOnlyDevicesWithAPushRegistration(t *testing.T) {
	store := devices.NewStore(t.TempDir(), func() time.Time { return fixedNow })
	withPush := pairWithPush(t, store, "phone", "g1")
	code, _, _ := store.NewPairingCode()
	if _, _, err := store.Pair(code, "no-push"); err != nil {
		t.Fatal(err)
	}

	regs, err := NewStoreRegistry(store).Registrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0].DeviceID != withPush.ID || regs[0].Grant != "g1" ||
		len(regs[0].Key) != 32 || regs[0].RelayURL != "https://relay.example" {
		t.Fatalf("registrations = %+v", regs)
	}
}

func TestRevokedDeviceIsNoLongerAPushTarget(t *testing.T) {
	store := devices.NewStore(t.TempDir(), func() time.Time { return fixedNow })
	keep := pairWithPush(t, store, "keep", "g-keep")
	gone := pairWithPush(t, store, "gone", "g-gone")
	reg := NewStoreRegistry(store)

	if err := store.Revoke(gone.ID); err != nil {
		t.Fatal(err)
	}

	regs, _ := reg.Registrations()
	if len(regs) != 1 || regs[0].DeviceID != keep.ID {
		t.Fatalf("after revoke, targets = %+v, want only %s", regs, keep.ID)
	}
}

func TestStoreRegistryForgetDropsTheRegistrationButKeepsTheDevice(t *testing.T) {
	store := devices.NewStore(t.TempDir(), func() time.Time { return fixedNow })
	dev := pairWithPush(t, store, "phone", "g1")
	reg := NewStoreRegistry(store)

	if err := reg.Forget(dev.ID, "g1"); err != nil {
		t.Fatal(err)
	}

	if regs, _ := reg.Registrations(); len(regs) != 0 {
		t.Fatalf("registration survived a 410: %+v", regs)
	}
	if list, _ := store.List(); len(list) != 1 {
		t.Fatalf("a 410 must not unpair the device: %+v", list)
	}
}
