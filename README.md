# fs-sim - Filesystem Simulator

A high-performance tool for generating and dynamically updating a simulated filesystem with realistic file structures, random ownership, and varied file types. Useful for testing backup systems, sync engines, and storage solutions.

## Features

- **Fast multi-threaded file creation** - configurable worker count for parallel I/O
- Creates large directory hierarchies with configurable depth and breadth
- Generates files with random content, sizes, and extensions
- Assigns random UIDs/GIDs and historical timestamps
- Dynamic update mode for ongoing filesystem changes

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

The `workers` setting controls parallelism. Set to `0` to auto-detect CPU count, or specify a value to match your storage system's capabilities:

```yaml
# Auto-detect (uses all CPUs)
workers: 0

# Fixed thread count
workers: 64
```

Higher thread counts can improve throughput on fast storage (NVMe, parallel filesystems), while lower counts may be better for spinning disks.

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

- Go 1.21+ (for building)
- UIDs/GIDs in config must exist on the system (see `create_users.sh`)
- Run as root for full ownership/permission functionality
