package inhibit

// NewLocker returns the real caffeinate-backed Locker.
func NewLocker() Locker { return caffeinateLocker{spawn: execSpawn} }
