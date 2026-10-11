package push

import (
	"encoding/base64"

	"github.com/blackpaw-studio/leo/internal/devices"
)

// Registration is one device's push credentials, decoded for sending.
type Registration struct {
	DeviceID string
	Grant    string
	Key      []byte
	RelayURL string
}

// Registry supplies the devices to push to and drops dead registrations.
type Registry interface {
	// Registrations returns every device with a push registration.
	Registrations() ([]Registration, error)
	// Forget removes deviceID's registration if it still carries grant.
	Forget(deviceID, grant string) error
}

// deviceStore is the slice of devices.Store a StoreRegistry uses.
type deviceStore interface {
	List() ([]devices.Device, error)
	ClearPushIfGrant(id, grant string) error
}

// StoreRegistry adapts the paired-device store to Registry. Reading the
// store on every call means a revoke from `leo devices revoke` (another
// process) or a re-registration takes effect on the next alert.
type StoreRegistry struct{ store deviceStore }

// NewStoreRegistry returns a Registry over store.
func NewStoreRegistry(store deviceStore) *StoreRegistry { return &StoreRegistry{store: store} }

func (r *StoreRegistry) Registrations() ([]Registration, error) {
	list, err := r.store.List()
	if err != nil {
		return nil, err
	}
	var out []Registration
	for _, d := range list {
		if d.Push == nil {
			continue
		}
		// An undecodable key yields a nil Key, which the sender skips.
		key, _ := base64.StdEncoding.DecodeString(d.Push.Key)
		out = append(out, Registration{DeviceID: d.ID, Grant: d.Push.Grant, Key: key, RelayURL: d.Push.RelayURL})
	}
	return out, nil
}

func (r *StoreRegistry) Forget(deviceID, grant string) error {
	return r.store.ClearPushIfGrant(deviceID, grant)
}
