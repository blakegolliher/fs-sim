# fs-sim - Filesystem Simulator

A high-performance tool for generating and dynamically updating a simulated filesystem with realistic file structures, random ownership, and varied file types. Useful for testing backup systems, sync engines, and storage solutions.

## Features

- **Fast multi-threaded file creation** - configurable worker count for parallel I/O
- Creates large directory hierarchies with configurable depth and breadth
- Generates files with random content, sizes, and extensions
- Assigns random UIDs/GIDs and historical timestamps
- Dynamic update mode for ongoing filesystem changes

## Download

Prebuilt static binaries are on the [releases page](https://github.com/blakegolliher/fs-sim/releases) (Linux x86_64/arm64, macOS Intel/Apple Silicon):

```bash
curl -LO https://github.com/blakegolliher/fs-sim/releases/latest/download/fs-sim-linux-amd64
curl -LO https://github.com/blakegolliher/fs-sim/releases/latest/download/config-quickstart.yaml
chmod +x fs-sim-linux-amd64

# edit base_dir in config-quickstart.yaml to point at your mount, then:
./fs-sim-linux-amd64 --config=config-quickstart.yaml populate
```

## Usage

### Build

```bash
go build -o fs-sim fs-sim.go
```

### Populate Mode

Creates the initial filesystem structure:

```bash
./fs-sim --config=config.yaml populate
```

### Update Mode

Performs random filesystem operations (create, delete, append, touch, metadata changes):

```bash
./fs-sim --config=config.yaml update
```

## Configuration

See `config.yaml` for all options. Key settings:

| Setting | Description |
|---------|-------------|
| `base_dir` | Root directory for simulated filesystem |
| `log_file` | Tracks created files for update mode |
| `target_dirs` | Number of directories to create |
| `target_files` | Number of files to create |
| `update_duration_seconds` | How long update mode runs |
| `workers` | Number of parallel threads (0 = auto-detect CPUs) |
| `action_weights` | Relative probability of each action type |

### Tuning Performance

The `workers` setting controls parallelism. File creation against NFS is
latency-bound, not CPU-bound: each create is a wire round-trip, so you want
many more workers than cores. Set to `0` to auto-size (4x CPU count, capped
at 256), or set it explicitly:

```yaml
# Auto (4x CPU count — good NFS default)
workers: 0

# Explicit — try 8-16x cores against a fast NFS array
workers: 128
```

Higher counts help fast storage (NVMe, parallel filesystems, scale-out NAS);
lower counts may be better for spinning disks.

### Tuning for NFS targets

- **Spread across directories.** Linux holds a per-directory kernel lock for
  every create — across the whole NFS round-trip — so creates only run in
  parallel when they target *different* directories. Populate mode schedules
  work directory-per-worker automatically. In torture mode, list several
  `flat_dirs`: they are all filled concurrently, and one flat dir caps
  create throughput at roughly 1/latency regardless of worker count.
- **Mount options.** `nconnect=16` multiplies TCP connections per mount.
  Two instances against two mounts of the same export (with `nosharecache`)
  sidestep the single-client directory lock for flat-dir tests.
- **Syscall budget.** Files and dirs are created with their final mode baked
  into `open`/`mkdir` (umask is cleared at startup), so most objects cost no
  chmod SETATTR. Timestamps are one SETATTR per file, applied after close;
  directory timestamps are restamped once at the end (phase 3), after the
  children that would have clobbered them.
- **Root is optional.** Only random uid/gid assignment needs root (chown is
  skipped otherwise); permissions and timestamps work as any user.

## Running Update Mode via Cron

To simulate ongoing filesystem activity, schedule the update mode to run periodically:

```bash
# Edit crontab
crontab -e

# Run every 5 minutes
*/5 * * * * /path/to/fs-sim --config=/path/to/config.yaml update >> /var/log/fs-sim.log 2>&1

# Run every hour
0 * * * * /path/to/fs-sim --config=/path/to/config.yaml update >> /var/log/fs-sim.log 2>&1
```

Adjust `update_duration_seconds` in your config to control how long each run lasts.

## Prerequisites

- Go 1.22+ (for building)
- UIDs/GIDs in config must exist on the system (see `create_users.sh`)
- Run as root for random ownership assignment (everything else works unprivileged)

## Tests

```bash
go test ./...
```

Covers exact file-count accounting, permission/timestamp application,
flat-dir parallel fills, and the log-writer failure path.
