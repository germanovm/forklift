# MTV-7442: Serialize LUN mapping per ESXi host in copy offload

## Summary

vSphere copy offload populators run in parallel (bounded by `MAX_POPULATOR_INFLIGHT`, default 20,
enforced per migration host). Each populator maps its target LUN to the migration host's initiator
group immediately when it starts. Some storage arrays cannot handle many concurrent LUN mapping
operations for the same host group — e.g. Hitachi arrays fail with
`Another LDEV is already mapped to LUN` when the concurrent LUN number auto-assignment races —
which fails the affected populator pods. Lowering `MAX_POPULATOR_INFLIGHT` works around the array
limitation but serializes the whole copy, slowing migrations down unacceptably.

## Problem

`RemoteEsxcliPopulator.Populate()` called `StorageApi.MapTarget()` (array-side LUN attach) before
acquiring the per-host lease. Only the ESXi rescan that follows the mapping was serialized via the
`HostLeaseLocker` Kubernetes Lease lock. With up to 20 populators in flight per host, up to 20
`MapTarget()` calls hit the same host group on the array at once:

- Arrays that auto-assign LUN numbers per host group (Vantara `AddPath` does not pass a LUN id)
  race on the assignment and return `Another LDEV is already mapped to LUN`.
- The failure aborts the populator pod, so the disk never populates.

## Solution

Move the `MapTarget()` call inside the existing per-host lease critical section, combining the LUN
mapping and the following rescan into a single serialized *attach* section:

- `mapAndRescan()` (new method in `internal/populator/remote_esxcli.go`) acquires the per-host
  lease, maps the LUN, and rescans the host, then releases the lease.
- The lease is keyed on the ESXi host — the same key the rescan already uses — because all in-flight
  populators for a host map LUNs to the same `xcopy-<hostID>` initiator group, and
  `MAX_POPULATOR_INFLIGHT` is enforced per host.
- The data copy (`vmkfstools` clone) is **not** under the lease: `MAX_POPULATOR_INFLIGHT` and the
  copy parallelism are unchanged. Only the short attach phase is serialized, at the lease slot
  concurrency (default 2).
- On failure the input LUN is returned unchanged so the existing partial cleanup can still attempt
  to unmap it.

The number of concurrent attach sections per host is bounded by the host lease slot count, which is
now configurable through the `HOST_LEASE_MAX_HOLDERS` environment variable (default `2`, matching
the previous rescan-only serialization; values below 1 are rejected). The variable follows the
existing `HOST_LEASE_NAMESPACE` / `HOST_LEASE_DURATION_SECONDS` pattern: it is read by the populator
from its environment and can be forwarded from the controller settings
(`settings.Migrations.HostLeaseMaxHolders` is copied into the per-disk storage secret by the vSphere
plan builder, like the other host lease settings).

## Alternatives considered

- Reduce `MAX_POPULATOR_INFLIGHT`: rejected — kills copy parallelism (explicitly called out in the
  ticket).
- Pass an explicit LUN id in each vendor `MapTarget` implementation: rejected — requires per-array
  changes (querying free LUN ids, race-free allocation) across ten vendor clients and does not help
  arrays that simply refuse concurrent mapping requests.
- A separate lease pool only for mapping: rejected — an extra lease acquisition per disk (each with
  the lease expiry turnaround) for no additional protection over combining map+rescan.

## Validation

- `go test ./internal/populator/` in `cmd/vsphere-copy-offload-populator`:
  - `mapAndRescan` specs verify the LUN is mapped and the host rescanned *inside* the lease keyed on
    the ESXi host, that a mapping failure returns the unresolved LUN for partial cleanup, and that
    the LUN is not mapped when the lease cannot be acquired.
  - `NewHostLeaseLocker` tests verify the `HOST_LEASE_MAX_HOLDERS` parsing (default, valid,
    zero/negative/non-numeric rejection).
- On a cluster with a supported array: run a plan with more disks than the array accepts concurrent
  mappings; all populators should now map serially per host and succeed without
  `Another LDEV is already mapped to LUN`, while copies still run `MAX_POPULATOR_INFLIGHT` in
  parallel.

## Assumptions

- The reported array failure is caused by concurrent mapping operations for the same host group;
  per-host serialization therefore removes it. Arrays that cannot even serialize two concurrent
  mappings on different host groups can set `HOST_LEASE_MAX_HOLDERS=1`.
- Cleanup-phase array calls (`UnmapTarget` / remap of the original groups) are not serialized: disks
  finish at staggered times, so the burst is much smaller than at attach time. If customers report
  the same array error during cleanup, the same lease pattern can wrap those calls as a follow-up.
- Operator (CRD/CSV) plumbing for `HOST_LEASE_MAX_HOLDERS` is a follow-up; today the setting can be
  delivered to populators through the controller environment → storage secret path like the other
  `HOST_LEASE_*` variables.
