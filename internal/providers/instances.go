package providers

import (
	"context"
	"fmt"

	"github.com/abcdlsj/gauge/internal/model"
)

// maxInstances bounds how many configurations one source can hold. The first
// slot keeps the historic settings keys, so existing installs need no
// migration; extra slots suffix every key with the slot number.
const maxInstances = 3

// InstanceSpec ties a source to the settings keys its instances use.
type InstanceSpec struct {
	Provider string
	BaseKey  string // credential key, used verbatim by slot 1
	Label    string // human name of the credential field
}

var instanceSpecs = []InstanceSpec{
	{Provider: "commandcode", BaseKey: "commandcode_api_key", Label: "API key"},
	{Provider: "opendesign", BaseKey: "opendesign_control_key", Label: "Control key"},
}

// InstanceSpecs lists the sources that support more than one configuration.
func InstanceSpecs() []InstanceSpec { return instanceSpecs }

// MaxInstances is the per-source ceiling including the first slot.
func MaxInstances() int { return maxInstances }

// SpecFor returns the instance metadata of a source.
func SpecFor(provider string) (InstanceSpec, bool) {
	for _, s := range instanceSpecs {
		if s.Provider == provider {
			return s, true
		}
	}
	return InstanceSpec{}, false
}

// InstanceKey returns the stored key for a slot. Slot 1 is the original key.
func InstanceKey(base string, slot int) string {
	if slot <= 1 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, slot)
}

// InstanceNameKey is where the user's display name for an extra instance lives.
func InstanceNameKey(provider string, slot int) string {
	return fmt.Sprintf("%s_account_%d", provider, slot)
}

// InstanceSlots lists the slots a source should collect: 1 always, plus any
// extra slot whose credential is stored.
func InstanceSlots(ctx context.Context, d *Deps, base string) []int {
	slots := []int{1}
	for slot := 2; slot <= maxInstances; slot++ {
		if v, ok, _ := d.Store.Get(ctx, InstanceKey(base, slot)); ok && v != "" {
			slots = append(slots, slot)
		}
	}
	return slots
}

// instanceName reads the display name of an extra instance.
func instanceName(ctx context.Context, d *Deps, provider string, slot int) string {
	if slot <= 1 {
		return ""
	}
	v, _, _ := d.Store.Get(ctx, InstanceNameKey(provider, slot))
	return v
}

// stampInstance tags snapshots with the slot suffix and the identity to show:
// the account name the API reported, else the display name the user typed.
func stampInstance(ctx context.Context, d *Deps, snaps []model.Snapshot, provider string, slot int, user string) {
	name := user
	if name == "" {
		name = instanceName(ctx, d, provider, slot)
	}
	for i := range snaps {
		if slot > 1 {
			snaps[i].Metric = fmt.Sprintf("%s@%d", snaps[i].Metric, slot)
		}
		if name != "" {
			snaps[i].Account = name
		}
	}
}
