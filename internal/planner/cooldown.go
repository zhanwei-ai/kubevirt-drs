package planner

import "time"

// EffectiveCooldown is how long after its last migration a VM is left alone. Three layers, each can only
// raise it (same shape as OpenShift's KubevirtMigrationAware plugin):
//
//	base   = max(c.Base, lastDuration)        heavier VMs (longer migrations) get longer protection
//	backoff = base * 2^(migrationsInWindow-1)  every further migration completed inside c.Window doubles it
//	result = min(backoff, c.Max)               unless c.Max is 0 (no cap)
//
// migrationsInWindow counts completed migrations inside the window including the last one; 0 or 1 means no
// backoff.
func EffectiveCooldown(lastDuration time.Duration, migrationsInWindow int, c Cooldown) time.Duration {
	base := c.Base
	if lastDuration > base {
		base = lastDuration
	}
	eff := base
	for i := 1; i < migrationsInWindow; i++ {
		eff *= 2
		if c.Max > 0 && eff >= c.Max {
			eff = c.Max
			break
		}
	}
	if c.Max > 0 && eff > c.Max {
		eff = c.Max
	}
	return eff
}

// InCooldown reports whether the VM may not be moved at `now`.
func InCooldown(vm *VM, c Cooldown, now time.Time) (bool, time.Duration) {
	if vm.LastMigrationEnd.IsZero() {
		return false, 0
	}
	eff := EffectiveCooldown(vm.LastMigrationDuration, vm.MigrationsInWindow, c)
	remaining := eff - now.Sub(vm.LastMigrationEnd)
	return remaining > 0, remaining
}
