# coretemp-exporter

A tiny Prometheus exporter that reads [Core Temp](https://www.alcpu.com/CoreTemp/)'s
shared-memory block and exposes CPU temperature / load / power as metrics. It exists
because the Ring0-driver-based readers (windows_exporter's `thermalzone`, OhmGraphite)
can't report CPU package temperature on this host, while Core Temp can.

It runs **on the Windows host** next to a running Core Temp instance and is scraped
like any other exporter (`fast-qwen:9184`).

## Metrics

| Metric | Labels | Description |
|---|---|---|
| `coretemp_up` | | 1 if the shared memory was read this scrape, else 0 |
| `coretemp_core_temperature_celsius` | `cpu`, `core` | Per-core temperature (°C) |
| `coretemp_core_load_ratio` | `cpu`, `core` | Per-core load (0–1) |
| `coretemp_tjmax_celsius` | `cpu` | Junction-max / throttle temperature |
| `coretemp_power_watts` | `cpu` | Package power (struct v2+) |
| `coretemp_tdp_watts` | `cpu` | TDP (struct v2+) |
| `coretemp_cpu_speed_mhz` / `coretemp_fsb_speed_mhz` / `coretemp_multiplier` / `coretemp_vid_volts` | | Clocks & voltage |
| `coretemp_core_count` / `coretemp_cpu_count` | | Topology |
| `coretemp_cpu_info` | `cpu_name` | Constant 1, CPU model in label |

Temperatures are always normalised to Celsius regardless of Core Temp's display unit.

## Build

Windows-only (uses the Win32 file-mapping API). Cross-compile from anywhere:

```sh
GOOS=windows GOARCH=amd64 go build -o coretemp-exporter.exe .
```

## Run

Core Temp must be running (its shared memory is created while the app is open).

```powershell
.\coretemp-exporter.exe --listen :9184
```

Allow the port through the firewall (once):

```powershell
New-NetFirewallRule -DisplayName "coretemp-exporter" -Direction Inbound `
  -Protocol TCP -LocalPort 9184 -Action Allow
```

### Run as a service

Core Temp itself is a desktop app, so the simplest setup is to launch both at logon.
To run the exporter headless as a service, use [NSSM](https://nssm.cc/):

```powershell
nssm install coretemp-exporter "C:\path\to\coretemp-exporter.exe" "--listen :9184"
nssm start coretemp-exporter
```

Note: Core Temp must be running in the same session that owns the shared memory for
the exporter to read it. Verify with `curl http://localhost:9184/metrics`.

## Prometheus

Already wired into `prometheus/prometheus.yml`:

```yaml
  - job_name: coretemp
    static_configs:
      - targets: ['fast-qwen:9184']
        labels:
          host: fast-qwen
```

The Windows Node Exporter Grafana dashboard's CPU temperature panels read from these
metrics (via the `coretemp_instance` template variable).
