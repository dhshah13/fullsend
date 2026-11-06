package install

// TestConcurrentStateAccess was removed — the exported State interface
// and PerRepoState type were dropped per #6170. Concurrency safety of
// the Driver interface is tested via TestComposedDriver_ConcurrentAllocateDeallocate.
